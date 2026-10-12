package main

import "testing"

func TestGenerationRequiresExplicitBillableOptIn(t *testing.T) {
	t.Setenv("ALLOW_EMBEDDING_GENERATION", "")
	if err := run(); err == nil {
		t.Fatal("generation allowed without opt-in")
	}
}

func TestGenerationRejectsBadBatchSizeBeforeCloud(t *testing.T) {
	t.Setenv("ALLOW_EMBEDDING_GENERATION", "true")
	t.Setenv("EMBEDDING_SIZE_BATCH", "0")
	if err := run(); err == nil {
		t.Fatal("zero batch size allowed")
	}
}
