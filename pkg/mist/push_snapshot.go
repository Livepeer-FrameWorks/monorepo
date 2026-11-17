package mist

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// PushSnapshot binds a complete active-writer inventory to one controller
// incarnation. An empty list from a replacement controller cannot prove that
// writers created by its predecessor have exited.
type PushSnapshot struct {
	ControllerID string
	Pushes       []PushInfo
}

func (c *Client) GetPushSnapshot(ctx context.Context) (PushSnapshot, error) {
	response, err := c.makeAPIRequestContext(ctx, map[string]interface{}{"config": true, "push_list": true})
	if err != nil {
		return PushSnapshot{}, err
	}
	return parsePushSnapshot(response)
}

func parsePushSnapshot(response map[string]interface{}) (PushSnapshot, error) {
	config, validConfig := response["config"].(map[string]interface{})
	if !validConfig {
		return PushSnapshot{}, fmt.Errorf("push inventory lacks controller configuration")
	}
	iid, validID := config["iid"].(string)
	if !validID || iid == "" || len(iid) > 128 || strings.IndexFunc(iid, unicode.IsControl) >= 0 {
		return PushSnapshot{}, fmt.Errorf("push inventory lacks a controller identity")
	}
	raw, present := response["push_list"]
	if !present {
		return PushSnapshot{}, fmt.Errorf("push inventory is missing")
	}
	if raw != nil {
		rows, ok := raw.([]interface{})
		if !ok {
			return PushSnapshot{}, fmt.Errorf("push inventory has an invalid shape")
		}
		seen := make(map[int]bool, len(rows))
		for _, row := range rows {
			fields, ok := row.([]interface{})
			if !ok || len(fields) < 4 {
				return PushSnapshot{}, fmt.Errorf("push inventory contains an incomplete writer")
			}
			id, validID := fields[0].(float64)
			stream, validStream := fields[1].(string)
			target, validTarget := fields[2].(string)
			if !validID || id <= 0 || id > math.MaxInt32 || math.Trunc(id) != id || !validStream || stream == "" || !validTarget || target == "" || seen[int(id)] {
				return PushSnapshot{}, fmt.Errorf("push inventory contains an invalid writer identity")
			}
			seen[int(id)] = true
		}
	}
	pushes, err := parsePushList(raw)
	if err != nil {
		return PushSnapshot{}, err
	}
	return PushSnapshot{ControllerID: iid, Pushes: pushes}, nil
}
