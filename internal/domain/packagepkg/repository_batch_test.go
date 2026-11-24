package packagepkg

import (
	"strings"
	"testing"
)

func TestInterfaceSpecBatchQueryUsesOnlyGeneratedPlaceholders(t *testing.T) {
	query, args := interfaceSpecBatchQuery([]int64{11, 22, 33}, true)
	if len(args) != 3 || args[0] != int64(11) || args[2] != int64(33) {
		t.Fatalf("batch args = %#v", args)
	}
	if !strings.Contains(query, "package_id in (?,?,?)") {
		t.Fatalf("batch query does not contain generated placeholders: %q", query)
	}
	if !strings.Contains(query, "and status=1") {
		t.Fatalf("enabled batch query is missing status filter: %q", query)
	}
	for _, unsafe := range []string{"11", "22", "33"} {
		if strings.Contains(query, unsafe) {
			t.Fatalf("batch query embedded ID %s instead of binding it: %q", unsafe, query)
		}
	}
}

func TestInterfaceSpecBatchQueryHandlesEmptyAndAdminLists(t *testing.T) {
	query, args := interfaceSpecBatchQuery(nil, true)
	if query != "" || args != nil {
		t.Fatalf("empty batch query = %q, args = %#v", query, args)
	}

	query, args = interfaceSpecBatchQuery([]int64{7}, false)
	if strings.Contains(query, "status=1") {
		t.Fatalf("admin batch query unexpectedly filtered disabled specs: %q", query)
	}
	if len(args) != 1 || args[0] != int64(7) {
		t.Fatalf("admin batch args = %#v", args)
	}
}

func TestAttachInterfacePackageSpecsKeepsPackagesIsolated(t *testing.T) {
	items := []InterfacePackage{{ID: "11"}, {ID: "22"}}
	attachInterfacePackageSpecs(items, []int64{11, 22}, map[int64][]InterfacePackageSpec{
		11: {
			{ID: "111", Price: "9.9000", SpecName: "starter"},
			{ID: "112", Price: "20.0000", SpecName: "premium"},
		},
		22: {
			{ID: "221", Price: "3.5000", SpecName: "daily"},
		},
	})

	if items[0].SpecCount != 2 || items[0].MinPrice != "9.9000" || items[0].Specs[0].ID != "111" {
		t.Fatalf("first package specs = %#v", items[0])
	}
	if items[1].SpecCount != 1 || items[1].MinPrice != "3.5000" || items[1].Specs[0].ID != "221" {
		t.Fatalf("second package specs = %#v", items[1])
	}
}
