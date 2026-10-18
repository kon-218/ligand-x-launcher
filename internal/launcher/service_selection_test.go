package launcher

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// B-234 L3: after "Update now" the services screen re-ticked default-on groups
// (Molecular Dynamics) that the user had deselected, so Continue pulled them.
func TestServiceSelectionKeepsTheUsersSavedGroups(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "frontend-public", "service_selection.js"))
	if err != nil {
		t.Fatal(err)
	}
	const groups = `[{"id":"core","required":true,"defaultOn":true},{"id":"docking","defaultOn":true},` +
		`{"id":"md","defaultOn":true},{"id":"admet","defaultOn":false},{"id":"qc","defaultOn":false,"locked":true}]`
	run := func(saved string, fromChange bool) []string {
		code := `const {initialServiceSelection} = require(process.argv[1]);` +
			`const s = initialServiceSelection(` + groups + `, ` + saved + `, ` + map[bool]string{true: "true", false: "false"}[fromChange] + `);` +
			`console.log(JSON.stringify([...s]))`
		out, err := exec.Command(node, "-e", code, script).Output()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		if err := json.Unmarshal(out, &ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	check := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", name, got, want)
			}
		}
	}
	check("first run", run(`[]`, false), "core", "docking", "md")
	check("after update, md deselected", run(`["core","docking"]`, true), "core", "docking")
	check("saved opt-in kept", run(`["core","admet"]`, true), "core", "admet")
	check("locked group never ticked", run(`["core","qc"]`, true), "core")
	check("nothing saved yet", run(`[]`, true), "core", "docking", "md")
}
