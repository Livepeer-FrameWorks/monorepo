package placement

// RequiresCommercialFacts inspects a validated effective policy, including every
// inherited hard constraint. Geography, ownership and cluster class do not imply
// a charging classification; unknown charging never means permanently free.
func RequiresCommercialFacts(policy *Policy) bool {
	if policy == nil {
		return false
	}
	for _, layer := range policy.Layers {
		if layer.Allow != nil {
			for _, selector := range layer.Allow.Any {
				if len(selector.Charging) != 0 {
					return true
				}
			}
		}
		for _, selector := range layer.Deny {
			if len(selector.Charging) != 0 {
				return true
			}
		}
	}
	for _, group := range policy.Groups {
		if group.Order == PriceFirst || len(group.Match.Charging) != 0 {
			return true
		}
	}
	return false
}
