package main

import "testing"

func TestUpsertBindingKeepsTheExistingCompositeForeignKey(t *testing.T) {
	parentID, existingID, key, value := "different-parent", "existing-parent", "dvr", "14400"
	parent := &table{byName: map[string]*column{"id": {format: "uuid"}}}
	child := &table{
		byName:     map[string]*column{"tier_id": {format: "uuid"}, "key": {format: "text"}, "value": {format: "jsonb"}},
		uniqueCols: map[string]bool{},
		uniqueSets: [][]string{{"tier_id", "key"}},
		fkColumns:  map[string]int{"tier_id": 0},
		fks:        []foreignKey{{columns: []string{"tier_id"}, parent: "purser.billing_tiers", parentColumns: []string{"id"}}},
	}
	for _, upsert := range []bool{false, true} {
		b := &binder{queryID: "purser:catalog", s: &seeder{
			tables: map[string]*table{"purser.billing_tiers": parent, "purser.tier_entitlements": child},
			samples: map[string][]map[string]*string{
				"purser.billing_tiers":     {{"id": &parentID}},
				"purser.tier_entitlements": {{"tier_id": &existingID, "key": &key, "value": &value}},
			},
		}}
		query := `INSERT INTO purser.tier_entitlements (tier_id, key, value) VALUES ($1, $2, $3::jsonb)`
		if upsert {
			query += ` ON CONFLICT (tier_id, key) DO UPDATE SET value = EXCLUDED.value`
		}
		bound := b.bind(query, []string{"tier_id", "key", "value"}, []string{"uuid", "text", "jsonb"})
		if upsert && (bound[0].Value != existingID || bound[1].Value != key || bound[2].Value != value) {
			t.Fatalf("upsert must exercise an existing composite key, got %+v", bound)
		}
		if !upsert && bound[0].Value != parentID {
			t.Fatalf("new insert must bind a live parent, got %+v", bound)
		}
	}
}
