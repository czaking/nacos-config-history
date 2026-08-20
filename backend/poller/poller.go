package poller

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"nacoshist/aliyun"
	"nacoshist/store"
)

type Poller struct {
	cl    *aliyun.Client
	st    *store.Store
	pages int // safety cap on history pages per config

	// overlap re-scans a little before the last watermark so a change landing
	// right at a poll boundary is never missed.
	overlap time.Duration
}

func New(cl *aliyun.Client, st *store.Store) *Poller {
	return &Poller{cl: cl, st: st, pages: 50, overlap: 2 * time.Minute}
}

// nsAllowlist parses ONLY_NAMESPACES (comma-separated namespace names or ids);
// empty means "all". Handy for local dev to sync a few real namespaces without
// pulling bot-churn noise. Returns nil when unset.
func nsAllowlist() map[string]bool {
	v := strings.TrimSpace(os.Getenv("ONLY_NAMESPACES"))
	if v == "" {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			m[p] = true
		}
	}
	return m
}

// Run performs one sync pass over every namespace.
//
// First time a namespace is seen it is *backfilled*: every config is enumerated
// and its full version history pulled. Thereafter each pass is *incremental*:
// ListConfigTrack (one cheap call per namespace, plus paging) reports which
// configs changed since the last watermark, and only those get a history fetch.
// This keeps steady-state polling to a handful of calls and avoids the MSE
// throttling that per-config enumeration triggered.
func (p *Poller) Run() error {
	start := time.Now()
	namespaces, err := p.cl.ListEngineNamespaces()
	if err != nil {
		return err
	}
	var totalNew int
	only := nsAllowlist()
	for _, ns := range namespaces {
		if only != nil && !only[ns.Name] && !only[ns.ID] {
			continue
		}
		if err := p.st.UpsertNamespace(ns.ID, ns.Name); err != nil {
			log.Printf("upsert ns %s: %v", ns.ID, err)
		}
		_, backfilled, err := p.st.GetSyncState(ns.ID)
		if err != nil {
			log.Printf("sync state ns=%s: %v", ns.ID, err)
			continue
		}
		var n int
		if !backfilled {
			n, err = p.backfillNamespace(ns)
		} else {
			n, err = p.incrementalNamespace(ns)
		}
		if err != nil {
			// Don't advance the watermark on failure — retry the same window
			// next pass rather than skip over missed changes.
			log.Printf("sync ns=%s (%s): %v", ns.Name, ns.ID, err)
			continue
		}
		if err := p.st.SetSyncState(ns.ID, start.UnixMilli(), true); err != nil {
			log.Printf("set sync state ns=%s: %v", ns.ID, err)
		}
		// One-time live sweep: after history sync, ensure every *current* config
		// has a live snapshot. Backfill/incremental only capture configs that have
		// (or gained) history; a config unchanged beyond Nacos's history retention
		// has no history row, so without this its current value is invisible.
		if err := p.liveSweepNamespace(ns); err != nil {
			log.Printf("live sweep ns=%s: %v", ns.Name, err)
		}
		totalNew += n
	}
	log.Printf("sync done: %d namespaces, %d new versions, %s",
		len(namespaces), totalNew, time.Since(start).Round(time.Millisecond))
	return nil
}

// backfillNamespace enumerates every config in a namespace and pulls its full
// history. Slow but one-time; throttling is handled by the client's spacing.
func (p *Poller) backfillNamespace(ns aliyun.Namespace) (int, error) {
	configs, err := p.cl.ListNacosConfigs(ns.ID)
	if err != nil {
		return 0, err
	}
	log.Printf("backfill ns=%s: %d configs", ns.Name, len(configs))
	var newCount int
	for _, cfg := range configs {
		n, err := p.syncConfigHistory(ns, cfg.DataID, cfg.Group)
		if err != nil {
			return newCount, err
		}
		newCount += n
	}
	return newCount, nil
}

// incrementalNamespace uses ListConfigTrack to find configs changed since the
// last watermark, then fetches history only for those.
func (p *Poller) incrementalNamespace(ns aliyun.Namespace) (int, error) {
	lastMs, _, err := p.st.GetSyncState(ns.ID)
	if err != nil {
		return 0, err
	}
	startSec := (lastMs - p.overlap.Milliseconds()) / 1000
	if startSec < 0 {
		startSec = 0
	}
	endSec := time.Now().Unix() + 1
	changed, err := p.cl.ListConfigTrack(ns.ID, startSec, endSec)
	if err != nil {
		return 0, err
	}
	var newCount int
	for _, c := range changed {
		n, err := p.syncConfigHistory(ns, c.DataID, c.Group)
		if err != nil {
			return newCount, err
		}
		newCount += n
	}
	if newCount > 0 {
		log.Printf("incremental ns=%s: %d changed configs, %d new versions",
			ns.Name, len(changed), newCount)
	}
	return newCount, nil
}

// syncConfigHistory pages through a config's history until it reaches versions
// already stored (by max nid), upserting the new ones.
func (p *Poller) syncConfigHistory(ns aliyun.Namespace, dataID, group string) (int, error) {
	maxKnown, err := p.st.MaxVersionNid(ns.ID, group, dataID)
	if err != nil {
		return 0, err
	}
	var newCount int
	for page := 1; page <= p.pages; page++ {
		items, total, err := p.cl.ListNacosHistoryConfigs(ns.ID, dataID, group, page, 100)
		if err != nil {
			return newCount, err
		}
		if len(items) == 0 {
			break
		}
		reachedKnown := false
		for _, v := range items {
			if v.Nid <= maxKnown {
				reachedKnown = true
				continue
			}
			if err := p.st.UpsertVersion(v, ns.ID, ns.Name); err != nil {
				log.Printf("upsert version %d: %v", v.Nid, err)
				continue
			}
			newCount++
		}
		// Stop once we've caught up to known versions or exhausted the list.
		if reachedKnown || int64(page*100) >= total {
			break
		}
	}
	// Refresh the config's current live value (not present in history) so the
	// timeline can show and diff against what's actually in effect now.
	p.snapshotLive(ns, dataID, group)
	return newCount, nil
}

// liveSweepMaxConfigs bounds the one-time live sweep. A namespace with more
// current configs than this is skipped (its live values still get captured
// on-change by the incremental pass) so a huge machine-written namespace like
// a huge namespace can not stall the poller for hours on first deploy behind thousands
// of GetNacosConfig calls. Override via LIVE_SWEEP_MAX_CONFIGS; 0 disables the
// cap. Default 1000 — comfortably above any hand-maintained namespace.
func liveSweepMaxConfigs() int {
	if v := strings.TrimSpace(os.Getenv("LIVE_SWEEP_MAX_CONFIGS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 1000
}

// liveSweepNamespace runs the namespace's one-time live sweep: enumerate every
// current config and seed a live snapshot for any that lacks one. Idempotent and
// self-healing — configs already snapshotted (via backfill or their last change)
// are skipped, so it costs one config listing plus a GetNacosConfig only for the
// gaps. Marked done on success so it doesn't re-enumerate every poll.
func (p *Poller) liveSweepNamespace(ns aliyun.Namespace) error {
	swept, err := p.st.GetLiveSwept(ns.ID)
	if err != nil || swept {
		return err
	}
	configs, err := p.cl.ListNacosConfigs(ns.ID)
	if err != nil {
		return err
	}
	if max := liveSweepMaxConfigs(); max > 0 && len(configs) > max {
		// Too big to sweep synchronously; leave live capture to the incremental
		// pass. Mark done so we don't re-enumerate every poll.
		log.Printf("live sweep ns=%s: %d configs exceeds cap %d, skipping bulk sweep",
			ns.Name, len(configs), max)
		return p.st.SetLiveSwept(ns.ID)
	}
	var seeded int
	for _, cfg := range configs {
		has, err := p.st.HasLive(ns.ID, cfg.Group, cfg.DataID)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		p.snapshotLive(ns, cfg.DataID, cfg.Group)
		seeded++
	}
	if seeded > 0 {
		log.Printf("live sweep ns=%s: seeded %d configs (of %d)", ns.Name, seeded, len(configs))
	}
	return p.st.SetLiveSwept(ns.ID)
}

// snapshotLive fetches a config's current live content and upserts it as the
// synthetic "current" version. Best-effort: a failure here must not fail the
// history sync. A missing config (deleted) yields no live row.
func (p *Poller) snapshotLive(ns aliyun.Namespace, dataID, group string) {
	content, md5, err := p.cl.GetNacosConfig(ns.ID, dataID, group)
	if err != nil {
		log.Printf("live snapshot ns=%s %s: %v", ns.Name, dataID, err)
		return
	}
	if content == "" && md5 == "" {
		return // config not present (e.g. deleted); nothing live to store
	}
	if err := p.st.UpsertLive(ns.ID, ns.Name, dataID, group, "", content, md5, time.Now().UnixMilli()); err != nil {
		log.Printf("upsert live ns=%s %s: %v", ns.Name, dataID, err)
	}
}
