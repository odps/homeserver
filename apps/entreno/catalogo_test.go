package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// catalogoPliega imita al matcher: minúsculas, sin tildes y solo letras y
// números separados por un espacio.
func catalogoPliega(s string) string {
	s = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", "à", "a", "è", "e", "ì", "i", "ò", "o", "ù", "u").Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }), " ")
}

func TestCatalogoCampos(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	kinds := []string{"reps", "time", "distance"}
	equipos := []string{"barra", "mancuerna", "maquina", "polea", "kettlebell", "peso corporal", "multipower", "banda", "otro"}
	// Sin carga externa por defecto, pero se les puede añadir (lastre, disco, mancuerna).
	lastrables := map[string]bool{
		"dip": true, "pull_up": true, "chin_up": true, "back_extension": true,
		"glute_bridge": true, "cossack_squat": true, "single_leg_calf_raise": true, "tibialis_raise": true,
		"decline_sit_up": true, "russian_twist": true,
	}
	if n := len(catalog); n < 130 || n > 160 {
		t.Errorf("el catálogo tiene %d ejercicios", n)
	}
	claves := map[string]bool{}
	for _, e := range catalog {
		if !snake.MatchString(e.Key) {
			t.Errorf("clave no snake_case: %q", e.Key)
		}
		if claves[e.Key] {
			t.Errorf("clave repetida: %q", e.Key)
		}
		claves[e.Key] = true
		if e.Name == "" || len(e.Aliases) == 0 {
			t.Errorf("%s: sin nombre o sin alias", e.Key)
		}
		if !slices.Contains(kinds, e.Kind) {
			t.Errorf("%s: Kind %q no válido", e.Key, e.Kind)
		}
		if !slices.Contains(equipos, e.Equipment) {
			t.Errorf("%s: Equipment %q no válido", e.Key, e.Equipment)
		}
		if len(e.Muscles) == 0 {
			t.Errorf("%s: sin músculos principales", e.Key)
		}
		for _, m := range append(slices.Clone(e.Muscles), e.Secondary...) {
			if !slices.Contains(muscleGroups, m) {
				t.Errorf("%s: grupo muscular %q no válido", e.Key, m)
			}
		}
		for _, m := range e.Secondary {
			if slices.Contains(e.Muscles, m) {
				t.Errorf("%s: %q es principal y secundario", e.Key, m)
			}
		}
		switch {
		case e.Step < 0:
			t.Errorf("%s: Step negativo", e.Key)
		case e.Bodyweight && e.Step > 0 && !lastrables[e.Key]:
			t.Errorf("%s: sin carga pero con Step %v", e.Key, e.Step)
		case !e.Bodyweight && e.Step == 0:
			t.Errorf("%s: con carga pero sin Step", e.Key)
		}
		if e.Equipment == "peso corporal" && !e.Bodyweight {
			t.Errorf("%s: peso corporal sin Bodyweight", e.Key)
		}
	}
}

func TestCatalogoAliasUnicos(t *testing.T) {
	exacto := map[string]string{}  // alias tal cual → clave
	plegado := map[string]string{} // alias o nombre plegado → clave
	junto := map[string]string{}   // plegado y sin espacios → clave
	apunta := func(m map[string]string, s, key, que string) {
		if s == "" {
			t.Errorf("%s: %s vacío", key, que)
			return
		}
		if otra, ok := m[s]; ok && otra != key {
			t.Errorf("%s %q apunta a %s y a %s", que, s, otra, key)
		}
		m[s] = key
	}
	for _, e := range catalog {
		f := catalogoPliega(e.Name)
		apunta(plegado, f, e.Key, "nombre")
		apunta(junto, strings.ReplaceAll(f, " ", ""), e.Key, "nombre")
		for _, a := range e.Aliases {
			if a != strings.ToLower(a) || a != strings.TrimSpace(a) {
				t.Errorf("%s: alias %q no está en minúsculas o tiene espacios", e.Key, a)
			}
			if _, ok := exacto[a]; ok {
				t.Errorf("alias repetido: %q", a)
			}
			exacto[a] = e.Key
			f := catalogoPliega(a)
			apunta(plegado, f, e.Key, "alias")
			apunta(junto, strings.ReplaceAll(f, " ", ""), e.Key, "alias")
		}
	}
}

// Cómo escribe la persona los ejercicios en sus notas reales y adónde van.
var catalogoNotas = map[string]string{
	"Lat": "lat_pulldown", "Lat machine": "lat_pulldown", "Lat pulldown": "lat_pulldown",
	"Banca pecho": "bench_press", "Banca": "bench_press", "Bench": "bench_press", "Bench press": "bench_press",
	"Military": "overhead_press", "Military press": "overhead_press",
	"Military dumbbell": "dumbbell_shoulder_press", "Overhead dumbell press": "dumbbell_shoulder_press",
	"Overhead dumbell": "dumbbell_shoulder_press", "Overhead dumbbell": "dumbbell_shoulder_press",
	"Pressa": "leg_press", "Pressa stretta": "leg_press", "Leg press": "leg_press",
	"Curl": "dumbbell_curl", "Curls": "dumbbell_curl", "Dumbell curls": "dumbbell_curl",
	"Dumbbell curls": "dumbbell_curl", "Bicep curl": "dumbbell_curl",
	"Lumbar": "back_extension", "Lumbar extensión": "back_extension",
	"Squat": "back_squat", "Back squat": "back_squat", "Squat multipower": "smith_squat",
	"Seated calf": "seated_calf_raise", "Seat calf": "seated_calf_raise", "Calf": "seated_calf_raise",
	"Stacco semitese": "stiff_leg_deadlift", "Semitese": "stiff_leg_deadlift", "SLD": "stiff_leg_deadlift",
	"Rdl": "romanian_deadlift", "Romanian dl": "romanian_deadlift", "Rom dl": "romanian_deadlift",
	"Rom deadlift": "romanian_deadlift", "Romanian": "romanian_deadlift",
	"Single leg Rdl": "single_leg_rdl", "Single rdl kettle": "single_leg_rdl", "Single leg rdl": "single_leg_rdl",
	"Addome": "decline_sit_up", "Situp": "sit_up", "Addome situp inclinado": "decline_sit_up",
	"Abdominal machine": "abdominal_machine",
	"Deadlift":          "deadlift", "Desdlift": "deadlift",
	"Aductor": "hip_adduction_machine", "Abductor": "hip_abduction_machine",
	"Aductor|Abductor": "hip_adduction_machine", "Aductor/Abductor turnado": "hip_adduction_machine",
	"Row": "seated_row", "Row machine": "seated_row", "Low row machine": "seated_row", "Low Row machine": "seated_row",
	"Barbell row": "barbell_row", "Dumbbell row": "dumbbell_row", "Dunbbell row": "dumbbell_row",
	"Chest supported rows": "chest_supported_row",
	"Bulgarian":            "bulgarian_split_squat", "Bulgaria": "bulgarian_split_squat", "Bulgarian Split": "bulgarian_split_squat",
	"Side plank": "side_plank", "Deadbug": "dead_bug", "Dead bug": "dead_bug", "Face pull": "face_pull",
	"Farmers Carry": "farmer_carry", "Farmer Carry": "farmer_carry", "Hip thrust": "hip_thrust",
	"Squat hold": "squat_hold", "Hollow hold": "hollow_hold", "Front squat": "front_squat",
	"Push ups": "push_up", "Push Up": "push_up", "Push UPS": "push_up",
	"Kettlebell": "kettlebell_swing", "Kettlebell swing": "kettlebell_swing",
	"Walking lunges": "walking_lunge", "Walking lunge": "walking_lunge",
	"Scapular push": "scapular_push_up", "Scapular pull": "scapular_pull",
	"Incline press": "incline_dumbbell_press", "Incline dumbell": "incline_dumbbell_press",
	"Incline dunbell": "incline_dumbbell_press", "Incline bench press": "incline_dumbbell_press",
	"Incline bench press 30°": "incline_dumbbell_press",
	"Lateral raise":           "lateral_raise", "Pull over polea": "cable_pullover",
	"Tibialis raise": "tibialis_raise", "Tríceps extensión": "overhead_cable_triceps_extension",
}

func TestCatalogoNotasReales(t *testing.T) {
	de := map[string]string{}
	for _, e := range catalog {
		for _, a := range e.Aliases {
			de[a] = e.Key
		}
	}
	for escrito, want := range catalogoNotas {
		got, ok := de[strings.ToLower(escrito)]
		if !ok {
			t.Errorf("falta el alias %q", strings.ToLower(escrito))
		} else if got != want {
			t.Errorf("%q → %s, quería %s", escrito, got, want)
		}
	}
}

func TestCatalogoTiempoYDistancia(t *testing.T) {
	want := map[string]string{
		"side_plank": "time", "hollow_hold": "time", "squat_hold": "time", "plank": "time",
		"wall_sit": "time", "dead_hang": "time", "farmer_carry": "distance", "sled_push": "distance",
		"dead_bug": "reps",
	}
	for key, kind := range want {
		e, ok := byKey(key)
		if !ok || e.Kind != kind {
			t.Errorf("%s: Kind %q, quería %q", key, e.Kind, kind)
		}
	}
	if e, _ := byKey("dead_bug"); !e.Bodyweight {
		t.Error("dead_bug debería ser sin carga")
	}
}

func TestCatalogoByKey(t *testing.T) {
	e, ok := byKey("back_squat")
	if !ok || e.Name != "Sentadilla trasera con barra" {
		t.Errorf("byKey(back_squat) = %+v, %v", e, ok)
	}
	for _, c := range catalog {
		if got, ok := byKey(c.Key); !ok || got.Key != c.Key {
			t.Errorf("byKey(%q) no lo encuentra", c.Key)
		}
	}
	if _, ok := byKey("no_existe"); ok {
		t.Error("byKey(no_existe) debería fallar")
	}
}
