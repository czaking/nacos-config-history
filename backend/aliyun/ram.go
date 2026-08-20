package aliyun

import (
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
)

// RAMUser maps a RAM principal (the SrcUser recorded by Nacos history) to a
// human-readable account name.
type RAMUser struct {
	PrincipalID string
	Username    string
}

// ListRAMUsers resolves principalId -> username via the RAM ListUsers API.
// Best-effort: callers should tolerate an error (the account may lack RAM read
// permission, in which case the UI falls back to showing the principalId).
func ListRAMUsers(ak, sk, token string) ([]RAMUser, error) {
	cfg := &openapi.Config{
		AccessKeyId:     tea.String(ak),
		AccessKeySecret: tea.String(sk),
		Endpoint:        tea.String("ram.aliyuncs.com"),
	}
	if token != "" {
		cfg.SecurityToken = tea.String(token)
		cfg.Type = tea.String("sts")
	}
	c, err := openapi.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	var out []RAMUser
	marker := ""
	for {
		q := map[string]*string{"MaxItems": tea.String("100")}
		if marker != "" {
			q["Marker"] = tea.String(marker)
		}
		params := &openapi.Params{
			Action:      tea.String("ListUsers"),
			Version:     tea.String("2015-05-01"),
			Protocol:    tea.String("HTTPS"),
			Method:      tea.String("POST"),
			AuthType:    tea.String("AK"),
			Style:       tea.String("RPC"),
			Pathname:    tea.String("/"),
			ReqBodyType: tea.String("formData"),
			BodyType:    tea.String("json"),
		}
		resp, err := c.CallApi(params, &openapi.OpenApiRequest{Query: q}, &util.RuntimeOptions{})
		if err != nil {
			return out, err
		}
		body, _ := resp["body"].(map[string]any)
		usersWrap, _ := body["Users"].(map[string]any)
		var users []any
		if usersWrap != nil {
			users, _ = usersWrap["User"].([]any)
		}
		for _, it := range users {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			name := asString(m["UserName"])
			if dn := asString(m["DisplayName"]); dn != "" {
				name = dn
			}
			out = append(out, RAMUser{
				PrincipalID: asString(m["UserId"]),
				Username:    name,
			})
		}
		if truncated, _ := body["IsTruncated"].(bool); truncated {
			marker = asString(body["Marker"])
			if marker != "" {
				continue
			}
		}
		break
	}
	return out, nil
}
