package herosms

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 生命周期动作。**62 一个都没有**——调用方必须按 provider 分流，
// 而不是把这几个动作当成通用能力。
const (
	ActionCancel     = "cancel"
	ActionFinish     = "finish"
	ActionReplace    = "replace"
	ActionReactivate = "reactivate"
	ActionProlong    = "prolong"
)

// Cancel 取消一个号码。严格 204 才算成功。
//
// **成功之后不自行改本地状态**：上游没说它变成了什么，我们就不替它说。
// 资源状态仍显示上游此前给的值，直到下一次同步把新状态读回来。
func (c *Client) Cancel(ctx context.Context, activationID string) error {
	id, err := safeActivationID(activationID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/activations/"+id, nil, nil)
}

// Finish 结束一个号码（已收到码、不再需要）。严格 204。
func (c *Client) Finish(ctx context.Context, activationID string) error {
	id, err := safeActivationID(activationID)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/activations/"+id+"/finish", nil, nil)
}

// Replace 换一个号码。
//
// **返回的是新的 activation，ID 可能与旧的不同**。调用方要把新 ID 落库，
// 而操作台账里仍保留原目标资源——那笔操作针对的是"那张旧号"，改写它会让
// 事后对账找不到起点。
func (c *Client) Replace(ctx context.Context, activationID string) (Activation, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return Activation{}, err
	}
	return c.activationAction(ctx, http.MethodPost, "/activations/"+id+"/replace", nil)
}

// ExtendOption 是 reactivate / prolong 的一个可选档位。
type ExtendOption struct {
	Duration int64
	Unit     string
	// Price 是十进制文本。
	Price string
}

// Fingerprint 是这个档位的本地指纹。
//
// **它不是供应商的签名，也不是价格锁**：上游的写体只带 duration。指纹的作用
// 是让"读选项"到"执行"之间本地参数没被改过——读到的价格与执行时的实际价格
// 是否一致，只能靠真实合同保证，代码给不了这个承诺。
func (o ExtendOption) Fingerprint() string {
	return fmt.Sprintf("%d|%s|%s", o.Duration, o.Unit, o.Price)
}

// GetReactivateOptions 读重新激活的可选档位（只读）。
func (c *Client) GetReactivateOptions(ctx context.Context, activationID string) ([]ExtendOption, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return nil, err
	}
	return c.extendOptions(ctx, "/activations/"+id+"/reactivate/options")
}

// GetProlongOptions 读延长的可选档位（只读）。
func (c *Client) GetProlongOptions(ctx context.Context, activationID string) ([]ExtendOption, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return nil, err
	}
	return c.extendOptions(ctx, "/activations/"+id+"/prolong/options")
}

// Reactivate 重新激活。**花钱的写操作。**
func (c *Client) Reactivate(ctx context.Context, activationID string, duration int) (Activation, error) {
	return c.durationAction(ctx, activationID, "reactivate", duration)
}

// Prolong 延长。**花钱的写操作。**
func (c *Client) Prolong(ctx context.Context, activationID string, duration int) (Activation, error) {
	return c.durationAction(ctx, activationID, "prolong", duration)
}

func (c *Client) durationAction(ctx context.Context, activationID, action string, duration int) (Activation, error) {
	id, err := safeActivationID(activationID)
	if err != nil {
		return Activation{}, err
	}
	if duration <= 0 {
		return Activation{}, connector.NewError(connector.KindRejected, "Hero-SMS 时长必须为正", nil)
	}
	// 上游写体**只带 duration**——unit 与 price 是本地预检用的，不发出去。
	body, err := json.Marshal(map[string]int{"duration": duration})
	if err != nil {
		return Activation{}, connector.NewError(connector.KindRejected, "编码 Hero-SMS 请求失败", err)
	}
	return c.activationAction(ctx, http.MethodPost, "/activations/"+id+"/"+action, body)
}

// extendOptions 读可选档位。
//
// **响应是 data.options 数组，而每项的 duration 是一个 {value, unit} 对象**
// ——不是一个标量。按标量解会得到 0，而 0 时长的档位在页面上看起来只是
// 「这个档位没写时长」，人会照选。
func (c *Client) extendOptions(ctx context.Context, path string) ([]ExtendOption, error) {
	var resp struct {
		Data struct {
			Options []struct {
				Price    json.Number `json:"price"`
				Duration struct {
					Value json.RawMessage `json:"value"`
					Unit  string          `json:"unit"`
				} `json:"duration"`
			} `json:"options"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Data.Options == nil {
		return nil, &ProtocolError{Kind: "档位列表缺失"}
	}

	out := make([]ExtendOption, 0, len(resp.Data.Options))
	for _, o := range resp.Data.Options {
		duration := jsonInt(o.Duration.Value)
		// 单位只认 minute / hour：出现别的说明上游加了新单位，
		// 而按分钟当小时算会让一次「延长 30」变成三十小时的账单。
		if duration <= 0 || (o.Duration.Unit != "minute" && o.Duration.Unit != "hour") {
			return nil, &ProtocolError{Kind: "档位时长或单位非法"}
		}
		out = append(out, ExtendOption{
			Duration: duration,
			Unit:     o.Duration.Unit,
			Price:    o.Price.String(),
		})
	}
	return out, nil
}

func (c *Client) activationAction(ctx context.Context, method, path string, body []byte) (Activation, error) {
	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.do(ctx, method, path, body, &resp); err != nil {
		return Activation{}, err
	}
	// 有的动作回单个对象，有的回数组，两种都收。
	if activations, err := parseActivations(resp.Data); err == nil && len(activations) == 1 {
		return activations[0], nil
	}
	var single map[string]any
	if err := decodeOneJSON(resp.Data, &single); err != nil {
		return Activation{}, &ProtocolError{Kind: "动作响应结构"}
	}
	wrapped, err := json.Marshal([]map[string]any{single})
	if err != nil {
		return Activation{}, &ProtocolError{Kind: "动作响应结构"}
	}
	activations, err := parseActivations(wrapped)
	if err != nil || len(activations) != 1 {
		return Activation{}, &ProtocolError{Kind: "动作响应结构"}
	}
	return activations[0], nil
}

// safeActivationID 把 ID 转成可安全拼进路径的形式。
func safeActivationID(activationID string) (string, error) {
	id := strings.TrimSpace(activationID)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/?#\r\n") {
		return "", connector.NewError(connector.KindRejected, "Hero-SMS activation ID 非法", nil)
	}
	return url.PathEscape(id), nil
}
