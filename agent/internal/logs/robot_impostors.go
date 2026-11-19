package logs

// WithoutImpostors вычитает тех, кто назвался роботом с чужого адреса (Р9):
// из суточных визитов — их число по `robot_checks`, из отказов — отказы
// самозванцам. «Пропал» и «отказывают» считаются по остатку — настоящим и
// тем, кого проверить нельзя. Сутки до нового агента проверки не знают, и
// вычитать там нечего.
func WithoutImpostors(visits []RobotCount, refusals []RobotRefusal, checks []RobotCheck, impostors []RobotRefusal) ([]RobotCount, []RobotRefusal) {
	type dayKey struct {
		day        int64
		host, name string
	}
	fake := map[dayKey]int{}
	for _, check := range checks {
		fake[dayKey{check.Day, check.Host, check.Name}] += check.Unverified
	}
	outVisits := make([]RobotCount, 0, len(visits))
	for _, count := range visits {
		if n := fake[dayKey{count.Day, count.Host, count.Name}]; n > 0 {
			count.Count -= n
			if count.Count < 0 {
				count.Count = 0
			}
		}
		outVisits = append(outVisits, count)
	}

	fakeRefusals := map[refusalKey]int{}
	for _, item := range impostors {
		fakeRefusals[refusalKey{item.Day, item.Host, item.Name, item.Status, item.RobotsTxt}] += item.Count
	}
	outRefusals := make([]RobotRefusal, 0, len(refusals))
	for _, item := range refusals {
		item.Count -= fakeRefusals[refusalKey{item.Day, item.Host, item.Name, item.Status, item.RobotsTxt}]
		if item.Count > 0 {
			outRefusals = append(outRefusals, item)
		}
	}
	return outVisits, outRefusals
}
