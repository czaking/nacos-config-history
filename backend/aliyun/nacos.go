package aliyun

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ---- response field helpers (JSON numbers arrive as float64) ----

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	case nil:
		return 0
	default:
		// Fallback: the OpenAPI SDK may hand back other numeric wrappers.
		n, _ := strconv.ParseInt(fmt.Sprint(t), 10, 64)
		return n
	}
}

// listField finds a slice in the body, preferring the given key but falling
// back to the first []any value present (MSE responses vary by action).
func listField(body map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := body[k].([]any); ok {
			return v
		}
		// some responses nest as {"Key":{"Item":[...]}}
		if m, ok := body[k].(map[string]any); ok {
			for _, vv := range m {
				if arr, ok := vv.([]any); ok {
					return arr
				}
			}
		}
	}
	return nil
}

// ---- domain types ----

type Namespace struct {
	ID   string `json:"id"`   // "" means the public/default namespace
	Name string `json:"name"`
}

type ConfigItem struct {
	DataID string `json:"dataId"`
	Group  string `json:"group"`
}

type Version struct {
	Nid        int64  `json:"nid"`
	DataID     string `json:"dataId"`
	Group      string `json:"group"`
	OpType     string `json:"opType"` // U update / I insert / D delete
	ModifiedMs int64  `json:"modifiedMs"`
	SrcUser    string `json:"srcUser"` // RAM principalId
	SrcIP      string `json:"srcIp"`
	AppName    string `json:"appName"`
}

// ---- API wrappers ----

func (cl *Client) ListEngineNamespaces() ([]Namespace, error) {
	body, err := cl.call("ListEngineNamespaces", map[string]any{
		"InstanceId": cl.InstanceID,
	})
	if err != nil {
		return nil, err
	}
	var out []Namespace
	for _, it := range listField(body, "Data") {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		id := asString(m["Namespace"])
		if id == "" {
			id = asString(m["NamespaceId"])
		}
		name := asString(m["NamespaceShowName"])
		if name == "" {
			name = asString(m["NamespaceDesc"])
		}
		if name == "" {
			name = id
		}
		out = append(out, Namespace{ID: id, Name: name})
	}
	return out, nil
}

// ListNacosConfigs returns all configs in a namespace, walking pagination.
func (cl *Client) ListNacosConfigs(nsID string) ([]ConfigItem, error) {
	var out []ConfigItem
	page := 1
	for {
		body, err := cl.call("ListNacosConfigs", map[string]any{
			"InstanceId":  cl.InstanceID,
			"NamespaceId": nsID,
			"PageNum":     page,
			"PageSize":    100,
		})
		if err != nil {
			return nil, err
		}
		items := listField(body, "Configurations", "ConfigurationsList", "Data")
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			grp := asString(m["Group"])
			if grp == "" {
				grp = "DEFAULT_GROUP"
			}
			out = append(out, ConfigItem{DataID: asString(m["DataId"]), Group: grp})
		}
		total := asInt64(body["TotalCount"])
		if int64(len(out)) >= total || len(items) == 0 {
			break
		}
		page++
	}
	return out, nil
}

// ListNacosHistoryConfigs returns one page of a config's version history.
func (cl *Client) ListNacosHistoryConfigs(nsID, dataID, group string, pageNum, pageSize int) ([]Version, int64, error) {
	body, err := cl.call("ListNacosHistoryConfigs", map[string]any{
		"InstanceId":  cl.InstanceID,
		"NamespaceId": nsID,
		"DataId":      dataID,
		"Group":       group,
		"PageNum":     pageNum,
		"PageSize":    pageSize,
	})
	if err != nil {
		return nil, 0, err
	}
	var out []Version
	for _, it := range listField(body, "HistoryItems") {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, Version{
			Nid:        asInt64(m["Id"]),
			DataID:     asString(m["DataId"]),
			Group:      asString(m["Group"]),
			OpType:     asString(m["OpType"]),
			ModifiedMs: asInt64(m["LastModifiedTime"]),
			SrcUser:    asString(m["SrcUser"]),
			SrcIP:      asString(m["SrcIp"]),
			AppName:    asString(m["AppName"]),
		})
	}
	return out, asInt64(body["TotalCount"]), nil
}

// TrackedConfig identifies a config that changed within a time window,
// discovered cheaply via ListConfigTrack (one call per namespace). It carries
// no operator or version id — those come from ListNacosHistoryConfigs, which we
// then call only for these changed configs.
type TrackedConfig struct {
	DataID string
	Group  string
}

// ListConfigTrack returns the set of configs that changed in a namespace within
// [startSec, endSec] (unix seconds). It is the cheap primitive that lets the
// incremental poller avoid hammering ListNacosHistoryConfigs for every config.
func (cl *Client) ListConfigTrack(nsID string, startSec, endSec int64) ([]TrackedConfig, error) {
	seen := map[string]TrackedConfig{}
	page := 1
	const pageSize = 100
	for {
		body, err := cl.call("ListConfigTrack", map[string]any{
			"InstanceId":  cl.InstanceID,
			"NamespaceId": nsID,
			"StartTs":     startSec,
			"EndTs":       endSec,
			"PageNum":     page,
			"PageSize":    pageSize,
		})
		if err != nil {
			return nil, err
		}
		traces := listField(body, "Traces")
		for _, it := range traces {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			grp := asString(m["Group"])
			if grp == "" {
				grp = "DEFAULT_GROUP"
			}
			dataID := asString(m["DataId"])
			key := grp + "\x00" + dataID
			seen[key] = TrackedConfig{DataID: dataID, Group: grp}
		}
		total := asInt64(body["TotalCount"])
		if int64(page*pageSize) >= total || len(traces) == 0 {
			break
		}
		page++
	}
	out := make([]TrackedConfig, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	return out, nil
}

// GetNacosHistoryConfig fetches the full content (and md5) of one version.
func (cl *Client) GetNacosHistoryConfig(nsID, dataID, group string, nid int64) (content, md5 string, err error) {
	body, err := cl.call("GetNacosHistoryConfig", map[string]any{
		"InstanceId":  cl.InstanceID,
		"NamespaceId": nsID,
		"DataId":      dataID,
		"Group":       group,
		"Nid":         nid,
	})
	if err != nil {
		return "", "", err
	}
	c, _ := body["Configuration"].(map[string]any)
	if c == nil {
		c = body // some responses inline the fields
	}
	return asString(c["Content"]), asString(c["Md5"]), nil
}

// GetNacosConfig fetches the current (live) content and md5 of a config — the
// value actually in effect now, which is NOT in the history table (history only
// records superseded snapshots). Used to store a synthetic "current" version so
// the timeline reflects live state.
func (cl *Client) GetNacosConfig(nsID, dataID, group string) (content, md5 string, err error) {
	body, err := cl.call("GetNacosConfig", map[string]any{
		"InstanceId":  cl.InstanceID,
		"NamespaceId": nsID,
		"DataId":      dataID,
		"Group":       group,
	})
	if err != nil {
		return "", "", err
	}
	c, _ := body["Configuration"].(map[string]any)
	if c == nil {
		c = body
	}
	return asString(c["Content"]), asString(c["Md5"]), nil
}
