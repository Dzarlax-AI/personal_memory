package memory

import "testing"

func TestMutationLocksAreScopedByNamespace(t *testing.T) {
	var server Server
	first := "projects"
	second := "work"
	for mutationStripe(first) == mutationStripe(second) {
		second += "-other"
	}

	unlockFirst := server.lockFactMutations(first)
	firstIndex := mutationStripe(first)
	if server.factMutationMu[firstIndex].TryLock() {
		server.factMutationMu[firstIndex].Unlock()
		t.Fatal("same namespace stripe was not locked")
	}

	secondIndex := mutationStripe(second)
	if !server.factMutationMu[secondIndex].TryLock() {
		t.Fatal("unrelated namespace stripe was blocked")
	}
	server.factMutationMu[secondIndex].Unlock()
	unlockFirst()
}

func TestMutationLockWithoutNamespaceCoversCollection(t *testing.T) {
	var server Server
	unlockAll := server.lockFactMutations()
	for index := range server.factMutationMu {
		if server.factMutationMu[index].TryLock() {
			server.factMutationMu[index].Unlock()
			t.Fatalf("collection lock omitted stripe %d", index)
		}
	}
	unlockAll()
}
