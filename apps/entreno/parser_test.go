package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeNames reconoce una lista fija de nombres (con tolerancia: palabras de
// más al final).
type fakeNames map[string]string

func (f fakeNames) exact(name string) (string, bool) { k, ok := f[fold(name)]; return k, ok }

func (f fakeNames) resolve(name string) (string, bool) {
	w := strings.Fields(fold(name))
	for n := len(w); n >= 1; n-- {
		if k, ok := f[strings.Join(w[:n], " ")]; ok {
			return k, true
		}
	}
	return "", false
}

var testNames = fakeNames{"lat machine": "lat_pulldown", "lat": "lat_pulldown", "farmer carry": "farmer_carry", "hollow hold": "hollow_hold",
	"scapular push": "scapular_push", "bulgarian": "bulgarian_split_squat", "seated calf": "seated_calf_raise", "military": "overhead_press",
	"bench press": "bench_press", "bench": "bench_press", "squat hold": "squat_hold", "squat multipower": "smith_squat", "squat": "back_squat", "back squat": "back_squat",
	"semitese": "stiff_leg_deadlift", "push ups": "push_up", "curl": "biceps_curl", "lumbar": "back_extension", "side plank": "side_plank",
	"incline bench press": "incline_db_press", "leg press": "leg_press", "rdl": "romanian_deadlift"}

func show(e exerciseLog) string {
	return fmt.Sprintf("%s[%s] %s", e.Key, e.Name, summarize(e)) + map[bool]string{true: " | " + e.Note, false: ""}[e.Note != ""]
}

func TestParseLine(t *testing.T) {
	cases := map[string]string{
		"Lat machine 3x10 50kg rir 2":                         "lat_pulldown[Lat machine] 3×10 · 50 kg · RIR 2",
		"Farmer Carry 3x20m 22kg":                             "farmer_carry[Farmer Carry] 3×20 m · 22 kg",
		"Hollow hold 3x45s":                                   "hollow_hold[Hollow hold] 3×45 s",
		"Scapular push 3x15":                                  "scapular_push[Scapular push] 3×15",
		"Bulgarian 3x8 12kg/ea rir 2":                         "bulgarian_split_squat[Bulgarian] 3×8 · 12 kg por lado · RIR 2",
		"Seated calf 20x2 15kg":                               "seated_calf_raise[Seated calf] 2×20 · 15 kg",
		"Military 1x8 3x6 Max 10kg":                           "overhead_press[Military] 1×8 · 10 kg + 3×6 · 10 kg",
		"Bench press 3x8 14kg fraccionado rir 0":              "bench_press[Bench press] 3×8 · 14 kg · RIR 0 | fraccionado",
		"Bench 3x8 50/55/60kg rpe 8":                          "bench_press[Bench] 1×8 · 50 kg · RIR 2 + 1×8 · 55 kg · RIR 2 + 1×8 · 60 kg · RIR 2",
		"Squat hold 2min rir 0":                               "squat_hold[Squat hold] 1×120 s · RIR 0",
		"Back squat 4x5 70kg rir n/a":                         "back_squat[Back squat] 4×5 · 70 kg",
		"Rdl 40kg 4x6 rír 2":                                  "romanian_deadlift[Rdl] 4×6 · 40 kg · RIR 2",
		"Rdl 4x6 4x6 50kg rir 0":                              "romanian_deadlift[Rdl] 4×6 · 50 kg · RIR 0",
		"Seated calf 12.5kg 3x30":                             "seated_calf_raise[Seated calf] 3×30 · 12.5 kg",
		"Incline bench press 30° 3x10 18kg rir 2":             "incline_db_press[Incline bench press 30°] 3×10 · 18 kg · RIR 2",
		"3x6 14kg bench press rir 2":                          "bench_press[bench press] 3×6 · 14 kg · RIR 2",
		"5x5 squat multipower, 50kg rir 2":                    "smith_squat[squat multipower] 5×5 · 50 kg · RIR 2",
		"4x15 15kg semitese":                                  "stiff_leg_deadlift[semitese] 4×15 · 15 kg",
		"3x8 push ups":                                        "push_up[push ups] 3×8",
		"Lumbar 2x20 Max 5kg , despacio":                     "back_extension[Lumbar] 2×20 · 5 kg | despacio",
		"Military dumbbell (estabilidad). 3x8 10kg":           "overhead_press[Military dumbbell] 3×8 · 10 kg",
		"Bench press 3x8 10kg bien, buena técnica": "bench_press[Bench press] 3×8 · 10 kg | bien, buena técnica",
		"3x30s side plank ea":                                 "side_plank[side plank] 3×30 s",
	}
	for in, want := range cases {
		p := parseLine(in, testNames)
		if p.Kind != lineExercise {
			t.Errorf("%q: tipo %v", in, p.Kind)
			continue
		}
		if got := show(p.Ex); got != want {
			t.Errorf("%q\n  da   %s\n  quiero %s", in, got, want)
		}
	}
	for in, want := range map[string]lineKind{
		".": lineNone, "A": lineNone, "Rir 2": lineRIR, "Rir lat 2": lineRIR, "3x8 8kg": lineMore, "40kg 4x6, cuesta abajo": lineMore,
		"Hoy con poca energía": lineNote, "Leg press averiada": lineNote, "Semana del 10 al 16, revisar coche": lineNote,
	} {
		if got := parseLine(in, testNames).Kind; got != want {
			t.Errorf("%q: tipo %v, quiero %v", in, got, want)
		}
	}
	if p := parseLine("Rir lat 2", testNames); p.For != "lat" || p.RIR == nil || *p.RIR != 2 {
		t.Errorf("Rir lat 2: %+v", p)
	}
}

func TestAssemble(t *testing.T) {
	lines := []string{"Lat 3x8 40kg", "Rir 2", "Bench press 3x8 14kg", "Lat controlado, bench controlado", "Rir lat 2", "Rir bench 1",
		"Military dumbbell (estabilidad). 3x8 10kg", "3x8 8kg", "Gimnasio lleno", "Rir 4", "Lumbar 2x20 5kg", "Lumbar rir 4"}
	exs, notes := assemble(lines, testNames)
	var got []string
	for _, e := range exs {
		got = append(got, show(e))
	}
	want := []string{
		"lat_pulldown[Lat] 3×8 · 40 kg · RIR 2",
		"bench_press[Bench press] 3×8 · 14 kg · RIR 1",
		"overhead_press[Military dumbbell] 3×8 · 10 kg · RIR 4 + 3×8 · 8 kg · RIR 4",
		"back_extension[Lumbar] 2×20 · 5 kg · RIR 4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ejercicios:\n%s\nquiero:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(notes, "|") != "Lat controlado, bench controlado|Gimnasio lleno" {
		t.Fatalf("notas: %q", notes)
	}
}

const sampleChat = `# WhatsApp Chat Export: Notas
Export date: 3 November 2026 at 22:04

---

## 21 January 2026

[20:40] **You:** .

[20:40] **You:** Squat 3x6 30kg libre sin multipower

[20:41] **You:** Hoy con poca energía

[21:22] **You:** Leg press 12x4 55kg

---

## 17 March 2026

[6:24] **You:** Comprar pilas

[6:24] **You:** Gimnasio

---

## 27 April 2026

[15:24] **You:** Ver una película

[19:59] **You:** Back squat 4x5 60kg rir 2

[20:31] **You:** Rdl 3x8 40kg rir 2
Bulgarian 3x8 24kg rir 0

---

## 7 May 2026

[17:47] **You:**
> _You: Rdl 4x6 50kg 4x6 rir 2
Squat hold 2min rir 0
Hollow hold 3x30s_
Lat machine 4x6 50kg rir 0

[21:24] **You:** _You deleted this message_

[22:07] **You:** [Image]
`

func TestChatImport(t *testing.T) {
	msgs := parseChat(sampleChat, time.UTC)
	sessions := chatSessions(msgs, testNames)
	var got []string
	for _, s := range sessions {
		line := s.Day.Format("2006-01-02") + " " + s.Start.Format("15:04") + ":"
		for _, e := range s.Exercises {
			line += " " + e.Key
		}
		if len(s.Notes) > 0 {
			line += " notas=" + strings.Join(s.Notes, "|")
		}
		got = append(got, line)
	}
	want := []string{
		"2026-01-21 20:40: back_squat leg_press notas=Hoy con poca energía",
		"2026-04-27 19:59: back_squat romanian_deadlift bulgarian_split_squat",
		"2026-05-07 17:47: lat_pulldown",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sesiones:\n%s\nquiero:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Formatos nativos de Android y iPhone.
	android := "12/05/26, 19:11 - Tú: Bench press 4x6 18kg rir 2\nLateral raise 3x12 10kg\n12/05/26, 22:00 - Tú: Película"
	if s := chatSessions(parseChat(android, time.UTC), testNames); len(s) != 1 || len(s[0].Exercises) != 2 || s[0].Exercises[1].Key != "" ||
		s[0].Day.Format("2006-01-02") != "2026-05-12" {
		t.Fatalf("android: %+v", s)
	}
	ios := "[12/05/26, 19:11:03] Tú: Bench press 4x6 18kg rir 2"
	if s := chatSessions(parseChat(ios, time.UTC), testNames); len(s) != 1 || s[0].Exercises[0].Key != "bench_press" {
		t.Fatalf("ios: %+v", s)
	}
}
