package main

// liveIDs are each kind's live rows, for the SKIP lines when the kind cannot run. Every case id a kind's live tier
// reports is here (liveids_test.go fails on one that is not).
var liveIDs = map[string][]string{
	"herdr":    {"H-L1", "H-L2", "H-L3", "H-L4", "H-L5", "H-L6", "H-L7", "H-L8", "H-L9", "H-L12", "H-L13", "H-L14"},
	"claude":   {"CC-L1", "CC-L2", "CC-L3", "CC-L4", "CC-L5", "CC-L6", "CC-L7", "CC-L8", "CC-L9", "H-L2"},
	"codex":    {"CX-L1", "CX-L2", "CX-L3", "CX-L4", "H-L11", "H-L14"},
	"opencode": {"OC-L1", "OC-L2", "OC-L3", "OC-L4", "OC-L5", "OC-L6", "OC-L7", "H-L14"},
	"agy":      {"AG-L1", "AG-L2", "AG-L3", "AG-L4", "AG-L5", "AG-L6", "AG-L7", "AG-L8", "AG-L9", "AG-L10", "AG-L11", "AG-L12", "AG-L13", "AG-L14", "AG-L15", "AG-L16", "AG-L17", "P10", "P10-control"},
}
