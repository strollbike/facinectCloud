package gateway

import "strings"

// canonicalPurpose maps caller labels onto the stored purpose.
// saas_<plan> stays intact so the plan code is not lost.
func canonicalPurpose(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "booking", "book":
		return "booking"
	case "coaching", "coach", "student":
		return "coaching"
	case "membership", "member":
		return "membership"
	case "tournament", "tournaments":
		return "tournament"
	case "fees", "fee":
		return "fees"
	case "subscription", "saas", "billing", "facility_plan":
		return "subscription"
	default:
		return s
	}
}

func isSubscriptionPurpose(purpose string) bool {
	p := strings.ToLower(strings.TrimSpace(purpose))
	return p == "subscription" || strings.HasPrefix(p, "saas_")
}

// customerPurpose is booking, coaching, membership, tournament, and mixed fee bills.
// Empty is allowed on the customer channel so an untagged Razorpay order is not dropped.
func isCustomerPurpose(purpose string) bool {
	switch canonicalPurpose(purpose) {
	case "booking", "coaching", "membership", "tournament", "fees", "":
		return true
	default:
		return false
	}
}

func channelAllows(channel, purpose string) bool {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "subscription":
		return isSubscriptionPurpose(purpose)
	case "razorpay":
		return isCustomerPurpose(purpose)
	default:
		return true
	}
}
