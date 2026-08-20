package store

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" driver for Postgres (prod)
	_ "modernc.org/sqlite"             // "sqlite" driver, pure-Go (local/test)

	"nacoshist/aliyun"
)

// LiveOpType marks the synthetic "current live value" row we store per config.
// It's distinct from Aliyun's I/U/D op types so we can include it in a config's
// timeline while excluding it from the change stream.
const LiveOpType = "LIVE"

// liveNid derives a stable, unique, negative synthetic nid for a config's live
// snapshot, so it never collides with Aliyun's positive history nids and is
// upserted (not accumulated) across polls. Masked to 51 bits so the magnitude
// stays below JS's MAX_SAFE_INTEGER (2^53-1) — otherwise the browser rounds the
// nid and diff/content lookups fail with "version not found".
func liveNid(nsID, group, dataID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(nsID))
	h.Write([]byte{0})
	h.Write([]byte(group))
	h.Write([]byte{0})
	h.Write([]byte(dataID))
	v := int64(h.Sum64() & 0x7FFFFFFFFFFFF) // 51 bits, JS-safe
	if v == 0 {
		v = 1
	}
	return -v
}

type Store struct {
	db     *sql.DB
	driver string
}

func Open(driver, dsn string) (*Store, error) {
	// modernc registers under "sqlite"; pgx stdlib registers under "pgx".
	sqlDriver := driver
	if driver == "sqlite" {
		sqlDriver = "sqlite"
	}
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	s := &Store{db: db, driver: driver}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// rebind converts ?-style placeholders to $1,$2,... for Postgres.
func (s *Store) rebind(q string) string {
	if s.driver != "pgx" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteString(fmt.Sprintf("$%d", n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (s *Store) exec(q string, args ...any) error {
	_, err := s.db.Exec(s.rebind(q), args...)
	return err
}

func (s *Store) migrate() error {
	// TEXT/BIGINT are accepted by both SQLite and Postgres. ON CONFLICT upserts
	// are supported by both. No AUTOINCREMENT needed: nid comes from Aliyun.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS namespaces (
			ns_id   TEXT PRIMARY KEY,
			ns_name TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			principal_id TEXT PRIMARY KEY,
			username     TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS versions (
			nid         BIGINT PRIMARY KEY,
			ns_id       TEXT,
			ns_name     TEXT,
			data_id     TEXT,
			grp         TEXT,
			op_type     TEXT,
			modified_ms BIGINT,
			src_user    TEXT,
			src_ip      TEXT,
			app_name    TEXT,
			md5         TEXT,
			content     TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_versions_modified ON versions(modified_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_versions_ns ON versions(ns_id)`,
		`CREATE INDEX IF NOT EXISTS idx_versions_config ON versions(ns_id, grp, data_id)`,
		`CREATE INDEX IF NOT EXISTS idx_versions_user ON versions(src_user)`,
		`CREATE TABLE IF NOT EXISTS sync_state (
			ns_id         TEXT PRIMARY KEY,
			last_track_ms BIGINT,
			backfilled    INTEGER
		)`,
		// Tracks whether a namespace has had its one-time live sweep — enumerate
		// every current config and seed its live snapshot — so already-backfilled
		// deployments surface current values for configs that haven't changed
		// within Nacos's history retention.
		`CREATE TABLE IF NOT EXISTS live_sweep_state (
			ns_id TEXT PRIMARY KEY,
			swept INTEGER
		)`,
	}
	for _, q := range stmts {
		if err := s.exec(q); err != nil {
			return fmt.Errorf("migrate: %w\n%s", err, q)
		}
	}
	return nil
}

func (s *Store) UpsertNamespace(id, name string) error {
	return s.exec(
		`INSERT INTO namespaces (ns_id, ns_name) VALUES (?, ?)
		 ON CONFLICT (ns_id) DO UPDATE SET ns_name = excluded.ns_name`,
		id, name)
}

// UpsertVersion inserts version metadata. Content is left untouched so a lazily
// fetched content is not clobbered on the next poll.
func (s *Store) UpsertVersion(v aliyun.Version, nsID, nsName string) error {
	return s.exec(
		`INSERT INTO versions
		   (nid, ns_id, ns_name, data_id, grp, op_type, modified_ms, src_user, src_ip, app_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (nid) DO UPDATE SET
		   ns_name = excluded.ns_name,
		   op_type = excluded.op_type,
		   modified_ms = excluded.modified_ms,
		   src_user = excluded.src_user,
		   src_ip = excluded.src_ip`,
		v.Nid, nsID, nsName, v.DataID, v.Group, v.OpType, v.ModifiedMs, v.SrcUser, v.SrcIP, v.AppName)
}

// MaxVersionNid returns the newest known version id for a config, or 0 if none.
// The poller uses it to stop paging once it reaches already-synced versions.
func (s *Store) MaxVersionNid(nsID, group, dataID string) (int64, error) {
	var nid sql.NullInt64
	err := s.db.QueryRow(s.rebind(
		`SELECT MAX(nid) FROM versions WHERE ns_id = ? AND grp = ? AND data_id = ?`),
		nsID, group, dataID).Scan(&nid)
	if err != nil {
		return 0, err
	}
	return nid.Int64, nil
}

// UpsertLive stores/refreshes the current live content of a config as a synthetic
// newest version row (negative nid, op_type LIVE). Unlike UpsertVersion it *does*
// overwrite content/md5/modified_ms, since the live value changes in place. The
// live value is not present in the history table, so this is the only way the
// timeline can show — and diff against — what's actually in effect now.
func (s *Store) UpsertLive(nsID, nsName, dataID, group, srcUser, content, md5 string, modifiedMs int64) error {
	return s.exec(
		`INSERT INTO versions
		   (nid, ns_id, ns_name, data_id, grp, op_type, modified_ms, src_user, src_ip, app_name, md5, content)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)
		 ON CONFLICT (nid) DO UPDATE SET
		   ns_name = excluded.ns_name,
		   modified_ms = excluded.modified_ms,
		   src_user = excluded.src_user,
		   md5 = excluded.md5,
		   content = excluded.content`,
		liveNid(nsID, group, dataID), nsID, nsName, dataID, group, LiveOpType,
		modifiedMs, srcUser, md5, content)
}

// HasLive reports whether a config already has a live snapshot row, so the
// one-time sweep can skip a redundant GetNacosConfig for configs whose live
// value was already captured (e.g. during backfill or on their last change).
func (s *Store) HasLive(nsID, group, dataID string) (bool, error) {
	var n int
	err := s.db.QueryRow(s.rebind(
		`SELECT COUNT(1) FROM versions WHERE ns_id = ? AND grp = ? AND data_id = ? AND op_type = ?`),
		nsID, group, dataID, LiveOpType).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetLiveSwept reports whether a namespace's one-time live sweep has completed.
func (s *Store) GetLiveSwept(nsID string) (bool, error) {
	var swept sql.NullInt64
	err := s.db.QueryRow(s.rebind(
		`SELECT swept FROM live_sweep_state WHERE ns_id = ?`), nsID).Scan(&swept)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return swept.Int64 != 0, nil
}

func (s *Store) SetLiveSwept(nsID string) error {
	return s.exec(
		`INSERT INTO live_sweep_state (ns_id, swept) VALUES (?, 1)
		 ON CONFLICT (ns_id) DO UPDATE SET swept = 1`, nsID)
}

// GetSyncState returns the last ListConfigTrack watermark (epoch ms) for a
// namespace and whether its initial full backfill has completed.
func (s *Store) GetSyncState(nsID string) (lastTrackMs int64, backfilled bool, err error) {
	var ms sql.NullInt64
	var bf sql.NullInt64
	err = s.db.QueryRow(s.rebind(
		`SELECT last_track_ms, backfilled FROM sync_state WHERE ns_id = ?`), nsID).
		Scan(&ms, &bf)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return ms.Int64, bf.Int64 != 0, nil
}

func (s *Store) SetSyncState(nsID string, lastTrackMs int64, backfilled bool) error {
	bf := 0
	if backfilled {
		bf = 1
	}
	return s.exec(
		`INSERT INTO sync_state (ns_id, last_track_ms, backfilled) VALUES (?, ?, ?)
		 ON CONFLICT (ns_id) DO UPDATE SET
		   last_track_ms = excluded.last_track_ms,
		   backfilled = excluded.backfilled`,
		nsID, lastTrackMs, bf)
}

func (s *Store) UpsertUser(principalID, username string) error {	return s.exec(
		`INSERT INTO users (principal_id, username) VALUES (?, ?)
		 ON CONFLICT (principal_id) DO UPDATE SET username = excluded.username`,
		principalID, username)
}

func (s *Store) SetContent(nid int64, content, md5 string) error {
	return s.exec(`UPDATE versions SET content = ?, md5 = ? WHERE nid = ?`, content, md5, nid)
}

func (s *Store) GetContent(nid int64) (content string, has bool, nsID, dataID, group string, err error) {
	var c sql.NullString
	err = s.db.QueryRow(s.rebind(
		`SELECT content, ns_id, data_id, grp FROM versions WHERE nid = ?`), nid).
		Scan(&c, &nsID, &dataID, &group)
	if err == sql.ErrNoRows {
		return "", false, "", "", "", fmt.Errorf("version %d not found", nid)
	}
	if err != nil {
		return "", false, "", "", "", err
	}
	return c.String, c.Valid, nsID, dataID, group, nil
}

// ---- query types for the API ----

type ChangeRow struct {
	Nid        int64  `json:"nid"`
	NsID       string `json:"nsId"`
	NsName     string `json:"nsName"`
	DataID     string `json:"dataId"`
	Group      string `json:"group"`
	OpType     string `json:"opType"`
	ModifiedMs int64  `json:"modifiedMs"`
	SrcUser    string `json:"srcUser"`
	Username   string `json:"username"`
	SrcIP      string `json:"srcIp"`
	Md5        string `json:"md5"`
}

// Changes returns version rows filtered by optional day/user/namespace/dataId.
// dayStartMs/dayEndMs are inclusive/exclusive epoch-ms bounds (0 = no bound).
func (s *Store) Changes(dayStartMs, dayEndMs int64, nsID, srcUser, dataID string, limit int) ([]ChangeRow, error) {
	q := `SELECT v.nid, v.ns_id, v.ns_name, v.data_id, v.grp, v.op_type,
	             v.modified_ms, v.src_user, COALESCE(u.username, ''), v.src_ip, COALESCE(v.md5,'')
	      FROM versions v LEFT JOIN users u ON u.principal_id = v.src_user
	      WHERE v.nid > 0` // exclude synthetic live rows from the change stream
	var args []any
	if dayStartMs > 0 {
		q += ` AND v.modified_ms >= ?`
		args = append(args, dayStartMs)
	}
	if dayEndMs > 0 {
		q += ` AND v.modified_ms < ?`
		args = append(args, dayEndMs)
	}
	if nsID != "" {
		q += ` AND v.ns_id = ?`
		args = append(args, nsID)
	}
	if srcUser != "" {
		q += ` AND v.src_user = ?`
		args = append(args, srcUser)
	}
	if dataID != "" {
		q += ` AND v.data_id = ?`
		args = append(args, dataID)
	}
	q += ` ORDER BY v.modified_ms DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := s.db.Query(s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeRow
	for rows.Next() {
		var r ChangeRow
		if err := rows.Scan(&r.Nid, &r.NsID, &r.NsName, &r.DataID, &r.Group, &r.OpType,
			&r.ModifiedMs, &r.SrcUser, &r.Username, &r.SrcIP, &r.Md5); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Versions returns the full version timeline of a single config, newest first.
func (s *Store) Versions(nsID, group, dataID string) ([]ChangeRow, error) {
	rows, err := s.db.Query(s.rebind(
		`SELECT v.nid, v.ns_id, v.ns_name, v.data_id, v.grp, v.op_type,
		        v.modified_ms, v.src_user, COALESCE(u.username,''), v.src_ip, COALESCE(v.md5,'')
		 FROM versions v LEFT JOIN users u ON u.principal_id = v.src_user
		 WHERE v.ns_id = ? AND v.grp = ? AND v.data_id = ?
		 ORDER BY v.modified_ms DESC`),
		nsID, group, dataID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeRow
	for rows.Next() {
		var r ChangeRow
		if err := rows.Scan(&r.Nid, &r.NsID, &r.NsName, &r.DataID, &r.Group, &r.OpType,
			&r.ModifiedMs, &r.SrcUser, &r.Username, &r.SrcIP, &r.Md5); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type NamespaceRow struct {
	NsID   string `json:"nsId"`
	NsName string `json:"nsName"`
}

// ConfigRow identifies a distinct config within a namespace.
type ConfigRow struct {
	DataID string `json:"dataId"`
	Group  string `json:"group"`
}

// Configs lists the distinct configs known in a namespace, including those that
// only have a live snapshot (no recorded history) — so the history-compare view
// can select a config that hasn't changed within Nacos's history retention.
func (s *Store) Configs(nsID string) ([]ConfigRow, error) {
	rows, err := s.db.Query(s.rebind(
		`SELECT DISTINCT data_id, grp FROM versions WHERE ns_id = ? ORDER BY data_id`), nsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigRow
	for rows.Next() {
		var r ConfigRow
		if err := rows.Scan(&r.DataID, &r.Group); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Namespaces() ([]NamespaceRow, error) {
	rows, err := s.db.Query(`SELECT ns_id, COALESCE(ns_name,'') FROM namespaces ORDER BY ns_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NamespaceRow
	for rows.Next() {
		var r NamespaceRow
		if err := rows.Scan(&r.NsID, &r.NsName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
