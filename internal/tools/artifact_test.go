package tools

import "testing"

func TestArtifactWriteAutoApprove(t *testing.T) {
	tool := NewArtifactWrite(nil)
	aa, ok := tool.(AutoApprover)
	if !ok || !aa.AutoApprove() {
		t.Fatal("artifact_write 应实现 AutoApprover")
	}
}
