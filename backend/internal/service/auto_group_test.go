package service

import "testing"

func TestAddGroupsFromOptionValue(t *testing.T) {
	groupCounts := map[string]int64{}

	addGroupsFromOptionValue(groupCounts, "GroupRatio", `{"default":1,"ProPlus":0.8}`)
	addGroupsFromOptionValue(groupCounts, "UserUsableGroups", `{"SuperPlus":"super tier"}`)
	addGroupsFromOptionValue(groupCounts, "GroupGroupRatio", `{"SpuerPlus":{"ProPlus":0.35,"UltraPlus":0.2}}`)
	addGroupsFromOptionValue(groupCounts, "group_ratio_setting.group_special_usable_group", `{"vip":{"+:special":"desc","-:removed":"remove","append":"desc"}}`)
	addGroupsFromOptionValue(groupCounts, "AutoGroups", `["AutoA","AutoB"]`)

	expected := []string{
		"default",
		"ProPlus",
		"SuperPlus",
		"SpuerPlus",
		"UltraPlus",
		"vip",
		"special",
		"removed",
		"append",
		"AutoA",
		"AutoB",
	}
	for _, groupName := range expected {
		if _, ok := groupCounts[groupName]; !ok {
			t.Fatalf("expected group %q to be parsed, got %#v", groupName, groupCounts)
		}
	}
	if _, ok := groupCounts["desc"]; ok {
		t.Fatalf("special usable group descriptions must not be parsed as group names: %#v", groupCounts)
	}
}

func TestAddGroupNamesSplitsCommaSeparatedGroups(t *testing.T) {
	groupCounts := map[string]int64{}

	addGroupNames(groupCounts, "default, ProPlus,UltraPlus", 3)

	for _, groupName := range []string{"default", "ProPlus", "UltraPlus"} {
		if groupCounts[groupName] != 3 {
			t.Fatalf("expected %q count to be 3, got %#v", groupName, groupCounts)
		}
	}
}
