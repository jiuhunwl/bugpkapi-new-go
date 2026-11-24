package advertising

import "testing"

func TestNormalizeAdvertisement(t *testing.T) {
	status := 1
	item, err := normalizeAdvertisement(SaveAdvertisementRequest{
		Name:        "首页活动",
		Placement:   PlacementAPIResponse,
		Title:       "新接口上线",
		Description: "查看本周新增能力",
		LinkURL:     "https://example.com/activity",
		LinkTarget:  "_blank",
		Payload:     map[string]any{"campaign": "launch"},
		SortNo:      10,
		StartTime:   "2026-08-07 10:00:00",
		EndTime:     "2026-08-08 10:00:00",
		Status:      &status,
	})
	if err != nil {
		t.Fatalf("normalizeAdvertisement() error=%v", err)
	}
	if item.Placement != PlacementAPIResponse || item.LinkTarget != "_blank" || item.Payload["campaign"] != "launch" {
		t.Fatalf("unexpected item: %#v", item)
	}
}

func TestNormalizeAdvertisementRejectsUnsafeValues(t *testing.T) {
	tests := []SaveAdvertisementRequest{
		{Name: "x", Placement: "unknown", Title: "x"},
		{Name: "x", Placement: PlacementHome, Title: "x", LinkURL: "javascript:alert(1)"},
		{Name: "x", Placement: PlacementHome, Title: "x", StartTime: "2026-08-08 10:00:00", EndTime: "2026-08-07 10:00:00"},
	}
	for _, test := range tests {
		if _, err := normalizeAdvertisement(test); err == nil {
			t.Fatalf("expected invalid request to fail: %#v", test)
		}
	}
}

func TestServiceProviderPlacementAndPublicContract(t *testing.T) {
	status := 1
	item, err := normalizeAdvertisement(SaveAdvertisementRequest{
		Name:      "内部服务商广告名称",
		Placement: "service_provider",
		Title:     "Example Cloud",
		LinkURL:   "https://example.com",
		Status:    &status,
	})
	if err != nil {
		t.Fatalf("normalizeAdvertisement() error=%v", err)
	}
	if item.Placement != PlacementService {
		t.Fatalf("placement=%q, want %q", item.Placement, PlacementService)
	}
	if item.Description != "" || item.ImageURL != "" || item.Payload != nil {
		t.Fatalf("service-provider placement retained page-only fields: %#v", item)
	}

	item.ID = "9007199254740993"
	public := publicAdvertisement(item)
	if public.ID != item.ID || public.Title != "Example Cloud" || public.LinkURL != "https://example.com" {
		t.Fatalf("unexpected public advertisement: %#v", public)
	}
}

func TestPickResponseAdvertisementUsesConfiguredRandomIndex(t *testing.T) {
	requestedLength := 0
	service := &Service{
		randomIndex: func(length int) int {
			requestedLength = length
			return 2
		},
	}
	items := []Advertisement{
		{ID: "101", Title: "广告 A"},
		{ID: "102", Title: "广告 B"},
		{ID: "103", Title: "广告 C"},
	}

	selected := service.pickResponseAdvertisement(items)
	if requestedLength != len(items) {
		t.Fatalf("random selector length=%d, want %d", requestedLength, len(items))
	}
	if selected.ID != "103" {
		t.Fatalf("selected advertisement=%q, want %q", selected.ID, "103")
	}
}

func TestPickResponseAdvertisementDoesNotRandomizeSingleItem(t *testing.T) {
	service := &Service{
		randomIndex: func(int) int {
			t.Fatal("random selector must not be called for a single item")
			return 0
		},
	}

	selected := service.pickResponseAdvertisement([]Advertisement{{ID: "201"}})
	if selected.ID != "201" {
		t.Fatalf("selected advertisement=%q, want %q", selected.ID, "201")
	}
}

func TestPickResponseAdvertisementHandlesEmptyList(t *testing.T) {
	selected := (&Service{}).pickResponseAdvertisement(nil)
	if selected.ID != "" {
		t.Fatalf("selected advertisement from empty list=%q, want empty", selected.ID)
	}
}
