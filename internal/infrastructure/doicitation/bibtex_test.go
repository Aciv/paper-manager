package doicitation

import "testing"

func TestInspectSingleBibTeXEntry(t *testing.T) {
	key, err := inspectSingleBibTeXEntry(`@article{provider-key,
  title = {A title with {nested} braces},
  note = "quoted } text"
}`)
	if err != nil {
		t.Fatal(err)
	}
	if key != "provider-key" {
		t.Fatalf("key = %q", key)
	}
	for _, invalid := range []string{
		"", "<html>no</html>", "@comment{x}",
		"@article{one,title={A}}\n@article{two,title={B}}",
		"@article{missing,title={A}",
	} {
		if _, err := inspectSingleBibTeXEntry(invalid); err == nil {
			t.Fatalf("inspectSingleBibTeXEntry(%q) unexpectedly succeeded", invalid)
		}
	}
}
