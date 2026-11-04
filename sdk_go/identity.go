package frameworks

import "github.com/Livepeer-FrameWorks/sdk-go/identity"

func (c *Client) identityName(query, name string) string {
	if _, ok := c.operationSince[name]; ok {
		return name
	}
	info, ok := operations[name]
	if !ok {
		return ""
	}
	hash, err := identity.Sum(query, name)
	if err != nil || hash != info.Identity {
		return ""
	}
	return name
}
