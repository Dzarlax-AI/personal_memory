package integrationbundle

import "regexp"

var writeRecommendationRef = regexp.MustCompile(`^[0-9a-f]{32}$`)

// WriteRecommendationReadRequest represents client-observed state only. It
// never creates or changes a reference and permits one lookup plus one
// delayed follow-up for a pending result.
type WriteRecommendationReadRequest struct {
	StoreFactSucceeded bool
	ToolDiscovered     bool
	OperationRef       string
	PreviousReads      int
	PreviousStatus     string
}

// ShouldReadWriteRecommendation bounds lookup to one read and one pending follow-up.
func ShouldReadWriteRecommendation(in WriteRecommendationReadRequest) bool {
	if !in.StoreFactSucceeded || !in.ToolDiscovered || !writeRecommendationRef.MatchString(in.OperationRef) {
		return false
	}
	switch {
	case in.PreviousReads == 0:
		return true
	case in.PreviousReads == 1:
		return in.PreviousStatus == "pending"
	default:
		return false
	}
}
