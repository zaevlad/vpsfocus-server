package vitals

import "testing"

// Точка истории несёт и остальное, у чего экран показывает «было → стало»
// (трек U), а у замера без подробностей (агент до 0.14.0) этих полей нет —
// не нули.
func TestPointCarriesDetails(t *testing.T) {
	ttfb := Metric{Value: 420, Rating: RatingGood}
	interactive := Metric{Value: 4800, Rating: RatingNeedsImprovement}
	report := Report{
		CheckedAt: "2026-09-29T10:00:00Z",
		Lab: &Lab{
			Score:       72,
			FCP:         Metric{Value: 1600, Rating: RatingGood},
			TTFB:        &ttfb,
			Interactive: &interactive,
			TotalBytes:  1_800_000,
			Categories: []Category{
				{ID: "performance", Score: 72},
				{ID: "accessibility", Score: 91},
			},
		},
	}

	point := report.Point()
	if point.LabFCP == nil || *point.LabFCP != 1600 {
		t.Errorf("FCP: %v", point.LabFCP)
	}
	if point.LabTTFB == nil || *point.LabTTFB != 420 {
		t.Errorf("ответ сервера: %v", point.LabTTFB)
	}
	if point.LabInteractive == nil || *point.LabInteractive != 4800 {
		t.Errorf("интерактивность: %v", point.LabInteractive)
	}
	if point.TotalBytes == nil || *point.TotalBytes != 1_800_000 {
		t.Errorf("вес: %v", point.TotalBytes)
	}
	if point.Categories["accessibility"] != 91 {
		t.Errorf("категории: %v", point.Categories)
	}

	bare := Report{Lab: &Lab{Score: 90}}.Point()
	if bare.LabTTFB != nil || bare.LabInteractive != nil || bare.TotalBytes != nil || bare.Categories != nil {
		t.Errorf("замеру без подробностей выданы нули: %+v", bare)
	}
}
