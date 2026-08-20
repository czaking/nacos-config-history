package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nacoshist/aliyun"
	"nacoshist/store"
)

type API struct {
	st  *store.Store
	cl  *aliyun.Client
	loc *time.Location // display timezone for day filtering
}

func New(st *store.Store, cl *aliyun.Client, loc *time.Location) *API {
	return &API{st: st, cl: cl, loc: loc}
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", a.health)
	mux.HandleFunc("/api/meta", a.meta)
	mux.HandleFunc("/api/namespaces", a.namespaces)
	mux.HandleFunc("/api/configs", a.configs)
	mux.HandleFunc("/api/changes", a.changes)
	mux.HandleFunc("/api/versions", a.versions)
	mux.HandleFunc("/api/content", a.content)
	mux.HandleFunc("/api/diff", a.diff)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// meta exposes the MSE instance id, region, and the console context params so
// the frontend can build deep links into the Aliyun MSE Nacos console. The
// cluster-context params (ClusterId, ClusterName, …) are fixed for this single
// instance; overridable via env for other deployments.
func (a *API) meta(w http.ResponseWriter, r *http.Request) {
	region := os.Getenv("ALIYUN_REGION")
	if region == "" {
		region = "us-west-1"
	}
	envOr := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	writeJSON(w, 200, map[string]any{
		"instanceId": a.cl.InstanceID,
		"region":     region,
		// These mirror the query params the MSE console carries on its config
		// detail route; without ClusterId/ClusterType the SPA can't resolve the
		// instance and the deep link lands on the wrong page.
		"consoleParams": map[string]string{
			"ClusterId":         envOr("MSE_CLUSTER_ID", "mse-201211e12"),
			"ClusterName":       envOr("MSE_CLUSTER_NAME", "your-nacos"),
			"ClusterType":       envOr("MSE_CLUSTER_TYPE", "Nacos-Ans"),
			"MseVersion":        envOr("MSE_VERSION", "mse_pro"),
			"VersionCode":       envOr("MSE_VERSION_CODE", "NACOS_2_3_2_1"),
			"AppVersion":        envOr("MSE_APP_VERSION", "2.3.2.1"),
			"prometheusVersion": envOr("MSE_PROM_VERSION", "basic"),
			"ChargeType":        envOr("MSE_CHARGE_TYPE", "POSTPAY"),
		},
	})
}

func (a *API) namespaces(w http.ResponseWriter, r *http.Request) {
	ns, err := a.st.Namespaces()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ns)
}

// configs: /api/configs?ns=<nsId> — distinct configs in a namespace, including
// live-only ones (no recorded history). Powers the history-compare config picker.
func (a *API) configs(w http.ResponseWriter, r *http.Request) {
	cfgs, err := a.st.Configs(r.URL.Query().Get("ns"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cfgs)
}

// changes: /api/changes?date=2026-07-31&ns=&user=&dataId=&limit=500
// date is interpreted in the server display timezone.
func (a *API) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var startMs, endMs int64
	if d := q.Get("date"); d != "" {
		day, err := time.ParseInLocation("2006-01-02", d, a.loc)
		if err != nil {
			writeErr(w, 400, "bad date, want YYYY-MM-DD")
			return
		}
		startMs = day.UnixMilli()
		endMs = day.Add(24 * time.Hour).UnixMilli()
	}
	// dateEnd extends the window to cover the whole end day [date 00:00, dateEnd 24:00).
	// Same timezone handling as date; used for an optional date range.
	if d := q.Get("dateEnd"); d != "" {
		day, err := time.ParseInLocation("2006-01-02", d, a.loc)
		if err != nil {
			writeErr(w, 400, "bad dateEnd, want YYYY-MM-DD")
			return
		}
		endMs = day.Add(24 * time.Hour).UnixMilli()
	}
	// explicit range overrides date
	if v := q.Get("startMs"); v != "" {
		startMs, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("endMs"); v != "" {
		endMs, _ = strconv.ParseInt(v, 10, 64)
	}
	limit := 500
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := a.st.Changes(startMs, endMs, q.Get("ns"), q.Get("user"), q.Get("dataId"), limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

// versions: /api/versions?ns=&group=DEFAULT_GROUP&dataId=
func (a *API) versions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group := q.Get("group")
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	rows, err := a.st.Versions(q.Get("ns"), group, q.Get("dataId"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows)
}

// contentByNid fetches a version's content, lazily pulling+caching from Aliyun
// on first access.
func (a *API) contentByNid(nid int64) (string, error) {
	content, has, nsID, dataID, group, err := a.st.GetContent(nid)
	if err != nil {
		return "", err
	}
	if has {
		return content, nil
	}
	c, md5, err := a.cl.GetNacosHistoryConfig(nsID, dataID, group, nid)
	if err != nil {
		return "", err
	}
	_ = a.st.SetContent(nid, c, md5)
	return c, nil
}

// content: /api/content?nid=123
func (a *API) content(w http.ResponseWriter, r *http.Request) {
	nid, err := strconv.ParseInt(r.URL.Query().Get("nid"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad nid")
		return
	}
	c, err := a.contentByNid(nid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"nid": nid, "content": c})
}

// diff: /api/diff?a=<nid1>&b=<nid2> — returns both contents for the frontend to
// diff. Any two versions can be compared, not just against the latest.
func (a *API) diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nidA, err1 := strconv.ParseInt(q.Get("a"), 10, 64)
	nidB, err2 := strconv.ParseInt(q.Get("b"), 10, 64)
	if err1 != nil || err2 != nil {
		writeErr(w, 400, "need a and b nids")
		return
	}
	ca, err := a.contentByNid(nidA)
	if err != nil {
		writeErr(w, 500, "version a: "+err.Error())
		return
	}
	cb, err := a.contentByNid(nidB)
	if err != nil {
		writeErr(w, 500, "version b: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"a": map[string]any{"nid": nidA, "content": ca},
		"b": map[string]any{"nid": nidB, "content": cb},
	})
}

// CORS is handy during local dev when frontend runs on a different port.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// staticDir strips /api routes handled elsewhere; used for serving the SPA.
func IsAPIPath(p string) bool { return strings.HasPrefix(p, "/api/") }
