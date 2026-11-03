package main

// Catálogo de ejercicios integrado. Solo datos: el reconocimiento de nombres
// (tildes, mayúsculas, erratas) está en match.go.

// exercise es un ejercicio del catálogo.
type exercise struct {
	Key        string   // estable, snake_case en inglés: "back_squat"
	Name       string   // nombre para enseñar, en español: "Sentadilla trasera con barra"
	Aliases    []string // cómo puede escribirse (minúsculas, sin tildes da igual): "back squat", "squat", "sentadilla"…
	Kind       string   // "reps" (repeticiones), "time" (segundos), "distance" (metros)
	Muscles    []string // grupos principales, de muscleGroups
	Secondary  []string // secundarios, de muscleGroups
	Equipment  string   // "barra", "mancuerna", "maquina", "polea", "kettlebell", "peso corporal", "multipower", "banda", "otro"
	Step       float64  // subida de carga típica en kg (2.5 barra, 2 mancuerna, 5 máquina/polea, 4 kettlebell, 0 sin carga)
	PerSide    bool     // se registra la carga por lado/mano (búlgaras, remo a una mano, paseo del granjero con dos pesos…)
	Bodyweight bool     // se hace sin carga externa por defecto (flexiones, dead bug…)
	Wger       int      // id del ejercicio en wger (0 si no hay equivalente)
	Fedb       string   // id en free-exercise-db ("" si no hay)
}

var muscleGroups = []string{"pecho", "espalda", "hombro", "biceps", "triceps", "antebrazo", "cuadriceps", "isquios", "gluteo", "aductores", "gemelo", "tibial", "abdomen", "lumbar", "trapecio", "cuerpo entero"}

// catalog es la lista de ejercicios conocidos. Los alias genéricos («row»,
// «curl», «calf», «lat», «kettlebell»…) van al ejercicio que la persona hace
// de verdad cuando escribe solo eso. Wger y Fedb apuntan al mismo ejercicio en
// datos/externos.json.gz (mismo movimiento, material, postura y lado); se dejan
// vacíos si lo más parecido es otro ejercicio.
var catalog = []exercise{
	// --- Pecho ---
	{
		Key: "bench_press", Name: "Press de banca con barra",
		Aliases: []string{"bench press", "bench", "banca", "banca pecho", "press banca", "press de banca", "barbell bench press", "bb bench", "flat bench", "panca piana", "distensioni su panca"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro"},
		Wger: 73, Fedb: "Barbell_Bench_Press_-_Medium_Grip",
	},
	{
		Key: "dumbbell_bench_press", Name: "Press de banca con mancuernas",
		Aliases: []string{"dumbbell bench press", "dumbell bench press", "db bench", "db bench press", "dumbbell press", "flat dumbbell press", "press banca mancuernas", "press con mancuernas", "panca piana manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro"},
		Wger: 75, Fedb: "Dumbbell_Bench_Press",
	},
	{
		// La persona lo hace con mancuernas (10–18 kg) en banco a 30°.
		Key: "incline_dumbbell_press", Name: "Press inclinado con mancuernas",
		Aliases: []string{"incline press", "incline dumbell", "incline dunbell", "incline dumbbell", "incline bench press", "incline bench press 30°", "incline dumbbell press", "incline dumbell press", "incline db press", "press inclinado", "press inclinado mancuernas", "press inclinado con mancuernas", "panca inclinata", "panca inclinata manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro", "triceps"},
		Wger: 537, Fedb: "Incline_Dumbbell_Press",
	},
	{
		Key: "incline_barbell_press", Name: "Press inclinado con barra",
		Aliases: []string{"incline barbell bench press", "incline barbell press", "press inclinado con barra", "press inclinado barra", "panca inclinata bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro", "triceps"},
		Wger: 538, Fedb: "Barbell_Incline_Bench_Press_-_Medium_Grip",
	},
	{
		Key: "decline_bench_press", Name: "Press declinado con barra",
		Aliases: []string{"decline bench press", "decline press", "press declinado", "panca declinata"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps"},
		Wger: 185, Fedb: "Decline_Barbell_Bench_Press",
	},
	{
		Key: "smith_bench_press", Name: "Press de banca en multipower",
		Aliases: []string{"smith bench press", "smith machine bench press", "bench multipower", "press banca multipower", "panca multipower"},
		Kind:    "reps", Equipment: "multipower", Step: 2.5,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro"},
		Fedb: "Smith_Machine_Bench_Press",
	},
	{
		Key: "machine_chest_press", Name: "Press de pecho en máquina",
		Aliases: []string{"chest press", "machine chest press", "chest press machine", "press de pecho", "press pecho maquina", "chest press macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro"},
		Wger: 129, Fedb: "Machine_Bench_Press",
	},
	{
		Key: "pec_deck", Name: "Contractor de pecho (pec deck)",
		Aliases: []string{"pec deck", "peck deck", "pec fly", "pec fly machine", "machine fly", "chest fly machine", "butterfly", "contractor", "contractor de pecho", "aperturas en maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro"},
		Wger: 135, Fedb: "Butterfly",
	},
	{
		Key: "cable_fly", Name: "Cruce de poleas",
		Aliases: []string{"cable fly", "cable flyes", "cable crossover", "crossover", "cruce de poleas", "cruces en polea", "aperturas en polea", "croci ai cavi"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro"},
		Wger: 323, Fedb: "Cable_Crossover",
	},
	{
		Key: "dumbbell_fly", Name: "Aperturas con mancuernas",
		Aliases: []string{"dumbbell fly", "dumbbell flyes", "dumbell fly", "db fly", "chest fly", "aperturas", "croci", "croci con manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro"},
		Wger: 238, Fedb: "Dumbbell_Flyes",
	},
	{
		Key: "incline_dumbbell_fly", Name: "Aperturas inclinadas con mancuernas",
		Aliases: []string{"incline dumbbell fly", "incline fly", "aperturas inclinadas", "croci panca inclinata"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"pecho"}, Secondary: []string{"hombro"},
		Wger: 308, Fedb: "Incline_Dumbbell_Flyes",
	},
	{
		Key: "push_up", Name: "Flexiones",
		Aliases: []string{"push ups", "push up", "pushups", "pushup", "flexiones de pecho", "flexion", "piegamenti", "flessioni"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro", "abdomen"},
		Wger: 1551, Fedb: "Pushups",
	},
	{
		Key: "incline_push_up", Name: "Flexiones inclinadas (manos elevadas)",
		Aliases: []string{"incline push up", "incline push ups", "flexiones inclinadas"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"pecho"}, Secondary: []string{"triceps", "hombro"},
		Wger: 1111, Fedb: "Incline_Push-Up",
	},
	{
		Key: "dip", Name: "Fondos en paralelas",
		Aliases: []string{"dips", "dip", "chest dips", "parallel bar dips", "fondos", "fondos paralelas", "dip alle parallele"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"pecho", "triceps"}, Secondary: []string{"hombro"},
		Wger: 194, Fedb: "Dips_-_Chest_Version",
	},
	{
		Key: "cable_pullover", Name: "Pullover en polea",
		Aliases: []string{"pull over polea", "pullover polea", "cable pullover", "pull over cable", "straight arm pulldown", "straight arm pull down", "jalon con brazos rectos", "pullover al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"pecho", "triceps"},
		Wger: 1137, Fedb: "Straight-Arm_Pulldown",
	},
	{
		Key: "dumbbell_pullover", Name: "Pullover con mancuerna",
		Aliases: []string{"dumbbell pullover", "pullover", "pull over", "pullover mancuerna", "pullover con manubrio"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"pecho", "espalda"}, Secondary: []string{"triceps"},
		Wger: 161, Fedb: "Straight-Arm_Dumbbell_Pullover",
	},

	// --- Espalda ---
	{
		Key: "lat_pulldown", Name: "Jalón al pecho",
		Aliases: []string{"lat", "lat machine", "lat pulldown", "lat pull down", "pulldown", "wide grip pulldown", "jalon", "jalon polea alta", "polea al pecho", "lat machine avanti"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps"},
		Wger: 1702, Fedb: "Wide-Grip_Lat_Pulldown",
	},
	{
		Key: "close_grip_pulldown", Name: "Jalón con agarre estrecho",
		Aliases: []string{"close grip pulldown", "close grip lat pulldown", "v bar pulldown", "neutral grip pulldown", "jalon agarre estrecho", "jalon estrecho", "lat machine presa stretta"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps"},
		Wger: 158, Fedb: "Close-Grip_Front_Lat_Pulldown",
	},
	{
		Key: "underhand_pulldown", Name: "Jalón con agarre supino",
		Aliases: []string{"reverse grip pulldown", "underhand pulldown", "supinated pulldown", "jalon supino", "jalon agarre supino", "lat machine presa inversa"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps"},
		Wger: 1127, Fedb: "Underhand_Cable_Pulldowns",
	},
	{
		Key: "pull_up", Name: "Dominadas",
		Aliases: []string{"pull up", "pull ups", "pullups", "pullup", "dominada", "trazioni", "trazioni alla sbarra"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "antebrazo"},
		Wger: 475, Fedb: "Pullups",
	},
	{
		Key: "chin_up", Name: "Dominadas supinas",
		Aliases: []string{"chin up", "chin ups", "chinups", "chinup", "dominadas agarre supino", "trazioni presa supina"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"espalda", "biceps"}, Secondary: []string{"antebrazo"},
		Wger: 152, Fedb: "Chin-Up",
	},
	{
		Key: "assisted_pull_up", Name: "Dominadas asistidas en máquina",
		Aliases: []string{"assisted pull up", "assisted pull ups", "assisted pullups", "dominadas asistidas", "gravitron", "trazioni assistite"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps"},
		Wger: 477,
	},
	{
		Key: "inverted_row", Name: "Remo invertido",
		Aliases: []string{"inverted row", "inverted rows", "australian pull up", "remo australiano", "rematore inverso"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "hombro"},
		Wger: 1198, Fedb: "Inverted_Row",
	},
	{
		Key: "barbell_row", Name: "Remo con barra",
		Aliases: []string{"barbell row", "barbell rows", "bent over row", "bent over barbell row", "bb row", "remo barra", "rematore con bilanciere", "rematore bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "lumbar", "trapecio"},
		Wger: 83, Fedb: "Bent_Over_Barbell_Row",
	},
	{
		Key: "t_bar_row", Name: "Remo en T",
		Aliases: []string{"t bar row", "t-bar row", "landmine row", "remo en t", "rematore t bar"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "trapecio", "lumbar"},
		Wger: 513, Fedb: "T-Bar_Row_with_Handle",
	},
	{
		Key: "dumbbell_row", Name: "Remo con mancuerna a una mano",
		Aliases: []string{"dumbbell row", "dunbbell row", "dumbell row", "db row", "one arm dumbbell row", "single arm dumbbell row", "remo con mancuerna", "remo mancuerna", "remo a una mano", "rematore con manubrio", "rematore manubrio"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "trapecio"},
		Wger: 81, Fedb: "One-Arm_Dumbbell_Row",
	},
	{
		Key: "chest_supported_row", Name: "Remo con apoyo en pecho",
		Aliases: []string{"chest supported rows", "chest supported row", "chest supported dumbbell row", "incline dumbbell row", "seal row", "remo con apoyo en pecho", "remo en banco inclinado", "remo apoyado"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "trapecio", "hombro"},
		Wger: 1283, Fedb: "Dumbbell_Incline_Row",
	},
	{
		// «Row» a secas es el remo sentado de la persona (máquina o polea baja).
		Key: "seated_row", Name: "Remo bajo sentado (polea o máquina)",
		Aliases: []string{"row", "rows", "row machine", "low row machine", "low row", "machine row", "seated row", "seated cable row", "cable row", "remo", "remo sentado", "remo en polea", "remo en polea baja", "remo bajo", "remo maquina", "remo gironda", "pulley", "pulley basso", "rematore al cavo basso", "low row macchina"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "trapecio"},
		Wger: 394, Fedb: "Seated_Cable_Rows",
	},
	{
		Key: "single_arm_cable_row", Name: "Remo a una mano en polea",
		Aliases: []string{"single arm cable row", "one arm cable row", "remo a una mano en polea", "remo unilateral polea"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps"},
		Wger: 1621, Fedb: "Seated_One-arm_Cable_Pulley_Rows",
	},
	{
		Key: "high_row", Name: "Remo alto en máquina",
		Aliases: []string{"high row", "high row machine", "remo alto", "remo alto maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"biceps", "trapecio"},
		Fedb: "Leverage_High_Row",
	},
	{
		Key: "face_pull", Name: "Face pull",
		Aliases: []string{"face pull", "face pulls", "facepull", "facepulls", "tirones a la cara", "jalon a la cara", "face pull al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio", "espalda"},
		Wger: 222, Fedb: "Face_Pull",
	},
	{
		// En la lat machine o colgado de la barra; puede llevar carga.
		Key: "scapular_pull", Name: "Retracción escapular (scapular pull)",
		Aliases: []string{"scapular pull", "scapular pulls", "scapular pull up", "scapular pull ups", "scap pull", "scap pull up", "depresion escapular", "retraccion escapular"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"espalda"}, Secondary: []string{"trapecio"},
		Wger: 1435, Fedb: "Scapular_Pull-Up",
	},
	{
		Key: "back_extension", Name: "Hiperextensiones lumbares",
		Aliases: []string{"lumbar", "lumbares", "lumbar extensión", "lumbar extension", "back extension", "back extensions", "hyperextension", "hyperextensions", "hiperextensiones", "hiperextension", "extensiones lumbares", "iperestensioni", "45 degree back extension"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"lumbar", "gluteo"}, Secondary: []string{"isquios"},
		Wger: 301, Fedb: "Hyperextensions_Back_Extensions",
	},
	{
		Key: "good_morning", Name: "Buenos días con barra",
		Aliases: []string{"good morning", "good mornings", "buenos dias", "good morning bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"isquios", "lumbar"}, Secondary: []string{"gluteo"},
		Fedb: "Good_Morning",
	},
	{
		Key: "bird_dog", Name: "Bird dog",
		Aliases: []string{"bird dog", "bird dogs", "birddog", "perro pajaro"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen", "lumbar"}, Secondary: []string{"gluteo"},
		Wger: 1572,
	},

	// --- Bisagra de cadera ---
	{
		Key: "deadlift", Name: "Peso muerto convencional",
		Aliases: []string{"deadlift", "desdlift", "deadlifts", "dl", "conventional deadlift", "peso muerto", "stacco", "stacco da terra", "stacco classico"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"isquios", "gluteo", "lumbar"}, Secondary: []string{"cuadriceps", "espalda", "trapecio", "antebrazo"},
		Wger: 184, Fedb: "Barbell_Deadlift",
	},
	{
		Key: "sumo_deadlift", Name: "Peso muerto sumo",
		Aliases: []string{"sumo deadlift", "sumo dl", "stacco sumo"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"gluteo", "cuadriceps", "aductores"}, Secondary: []string{"isquios", "lumbar", "trapecio"},
		Wger: 630, Fedb: "Sumo_Deadlift",
	},
	{
		Key: "trap_bar_deadlift", Name: "Peso muerto con barra hexagonal",
		Aliases: []string{"trap bar deadlift", "hex bar deadlift", "trap bar", "hex bar", "peso muerto barra hexagonal", "stacco trap bar"},
		Kind:    "reps", Equipment: "otro", Step: 2.5,
		Muscles: []string{"cuadriceps", "gluteo", "isquios"}, Secondary: []string{"lumbar", "trapecio", "antebrazo"},
		Wger: 1803, Fedb: "Trap_Bar_Deadlift",
	},
	{
		Key: "romanian_deadlift", Name: "Peso muerto rumano",
		Aliases: []string{"rdl", "romanian", "romanian dl", "rom dl", "rom deadlift", "romanian deadlift", "rumano", "stacco rumeno"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"isquios", "gluteo"}, Secondary: []string{"lumbar", "antebrazo"},
		Wger: 507, Fedb: "Romanian_Deadlift",
	},
	{
		Key: "dumbbell_rdl", Name: "Peso muerto rumano con mancuernas",
		Aliases: []string{"dumbbell rdl", "dumbell rdl", "db rdl", "dumbbell romanian deadlift", "rumano mancuernas", "stacco rumeno manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"isquios", "gluteo"}, Secondary: []string{"lumbar", "antebrazo"},
		Wger: 1652,
	},
	{
		Key: "stiff_leg_deadlift", Name: "Peso muerto piernas rígidas",
		Aliases: []string{"sld", "stiff leg deadlift", "stiff legged deadlift", "stiff leg dl", "stacco semitese", "semitese", "stacco gambe semitese", "stacco a gambe semitese", "stacco a gambe tese", "peso muerto piernas rectas"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"isquios", "gluteo"}, Secondary: []string{"lumbar"},
		Wger: 627, Fedb: "Stiff-Legged_Barbell_Deadlift",
	},
	{
		Key: "single_leg_rdl", Name: "Peso muerto rumano a una pierna",
		Aliases: []string{"single leg rdl", "single rdl kettle", "single rdl", "sl rdl", "single leg romanian deadlift", "single leg deadlift", "peso muerto a una pierna", "peso muerto unilateral", "rumano a una pierna", "stacco monopodalico"},
		Kind:    "reps", Equipment: "kettlebell", Step: 4, PerSide: true,
		Muscles: []string{"isquios", "gluteo"}, Secondary: []string{"lumbar", "abdomen"},
		Wger: 1641, Fedb: "Kettlebell_One-Legged_Deadlift",
	},
	{
		Key: "rack_pull", Name: "Peso muerto desde bloques (rack pull)",
		Aliases: []string{"rack pull", "rack pulls", "block pull", "peso muerto desde bloques"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"lumbar", "gluteo", "trapecio"}, Secondary: []string{"isquios", "antebrazo", "espalda"},
		Wger: 484, Fedb: "Rack_Pulls",
	},
	{
		Key: "kettlebell_swing", Name: "Swing con kettlebell",
		Aliases: []string{"kettlebell", "kettlebell swing", "kettlebell swings", "kb swing", "kb swings", "swing", "swings", "swing con pesa rusa", "pesa rusa"},
		Kind:    "reps", Equipment: "kettlebell", Step: 4,
		Muscles: []string{"gluteo", "isquios"}, Secondary: []string{"lumbar", "abdomen", "hombro"},
		Wger: 960,
	},
	{
		Key: "cable_pull_through", Name: "Pull through en polea",
		Aliases: []string{"cable pull through", "pull through", "pull-through", "tiron entre piernas"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios"},
		Wger: 1751, Fedb: "Pull_Through",
	},

	// --- Glúteo y cadera ---
	{
		Key: "hip_thrust", Name: "Hip thrust con barra",
		Aliases: []string{"hip thrust", "hip thrusts", "barbell hip thrust", "empuje de cadera", "elevacion de cadera con barra", "hip thrust bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios", "cuadriceps"},
		Wger: 294, Fedb: "Barbell_Hip_Thrust",
	},
	{
		Key: "machine_hip_thrust", Name: "Hip thrust en máquina",
		Aliases: []string{"hip thrust machine", "machine hip thrust", "hip thrust maquina", "hip thrust macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios"},
	},
	{
		Key: "glute_bridge", Name: "Puente de glúteo",
		Aliases: []string{"glute bridge", "glute bridges", "bridge", "puente gluteo", "ponte glutei"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios"},
		Wger: 265, Fedb: "Butt_Lift_Bridge",
	},
	{
		Key: "single_leg_glute_bridge", Name: "Puente de glúteo a una pierna",
		Aliases: []string{"single leg glute bridge", "single leg bridge", "puente a una pierna", "puente de gluteo unilateral"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios", "abdomen"},
		Wger: 1740, Fedb: "Single_Leg_Glute_Bridge",
	},
	{
		Key: "cable_glute_kickback", Name: "Patada de glúteo en polea",
		Aliases: []string{"cable kickback", "glute kickback", "cable glute kickback", "patada de gluteo", "patada gluteo polea", "kickback gluteo", "slanci al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"gluteo"}, Secondary: []string{"isquios"},
		Wger: 1131, Fedb: "One-Legged_Cable_Kickback",
	},
	{
		Key: "hip_abduction_machine", Name: "Abductores en máquina",
		Aliases: []string{"abductor", "abductor machine", "abductor maquina", "abductores", "hip abduction", "hip abduction machine", "maquina abductores", "abduttori"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"gluteo"},
		Wger:    1748, Fedb: "Thigh_Abductor",
	},
	{
		// «Aductor/Abductor» en las notas es la máquina doble usada para aductores.
		Key: "hip_adduction_machine", Name: "Aductores en máquina",
		Aliases: []string{"aductor", "aductor|abductor", "aductor/abductor turnado", "aductor/abductor", "aductor abductor", "adductor", "adductor machine", "aductor maquina", "aductores", "hip adduction", "hip adduction machine", "maquina aductores", "adduttori"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"aductores"},
		Wger:    12, Fedb: "Thigh_Adductor",
	},
	{
		Key: "clamshell", Name: "Almeja con banda (clamshell)",
		Aliases: []string{"clamshell", "clamshells", "clam", "almeja", "almejas"},
		Kind:    "reps", Equipment: "banda", Bodyweight: true,
		Muscles: []string{"gluteo"},
		Wger:    1842,
	},
	{
		Key: "band_lateral_walk", Name: "Paso lateral con banda",
		Aliases: []string{"lateral band walk", "band walk", "monster walk", "lateral walk", "paso lateral", "pasos laterales con goma"},
		Kind:    "reps", Equipment: "banda", Bodyweight: true,
		Muscles: []string{"gluteo"},
		Wger:    1514,
	},
	{
		Key: "copenhagen_plank", Name: "Plancha Copenhague",
		Aliases: []string{"copenhagen plank", "copenhagen", "plancha copenhague", "copenaghen"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"aductores"}, Secondary: []string{"abdomen"},
		Wger: 1605,
	},

	// --- Cuádriceps / sentadillas ---
	{
		Key: "back_squat", Name: "Sentadilla trasera con barra",
		Aliases: []string{"squat", "squats", "back squat", "sentadilla", "sentadillas", "sentadilla trasera", "sentadilla con barra", "high bar squat", "low bar squat", "barbell squat", "squat bilanciere", "accosciata"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores", "lumbar", "abdomen"},
		Wger: 1801, Fedb: "Barbell_Squat",
	},
	{
		Key: "front_squat", Name: "Sentadilla frontal",
		Aliases: []string{"front squat", "front squats", "squat frontale"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"cuadriceps"}, Secondary: []string{"gluteo", "abdomen", "lumbar"},
		Wger: 257, Fedb: "Front_Barbell_Squat",
	},
	{
		Key: "smith_squat", Name: "Sentadilla en multipower",
		Aliases: []string{"squat multipower", "smith squat", "smith machine squat", "sentadilla multipower", "sentadilla smith", "squat al multipower"},
		Kind:    "reps", Equipment: "multipower", Step: 2.5,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores"},
		Wger: 1747, Fedb: "Smith_Machine_Squat",
	},
	{
		Key: "goblet_squat", Name: "Sentadilla goblet",
		Aliases: []string{"goblet squat", "goblet", "sentadilla copa", "squat goblet"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores", "abdomen"},
		Wger: 203,
	},
	{
		Key: "hack_squat", Name: "Hack squat (máquina)",
		Aliases: []string{"hack squat", "hack", "hack squat machine", "sentadilla hack", "hack squat macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"cuadriceps"}, Secondary: []string{"gluteo"},
		Wger: 1414, Fedb: "Hack_Squat",
	},
	{
		Key: "pendulum_squat", Name: "Sentadilla péndulo",
		Aliases: []string{"pendulum squat", "pendulum", "pendulum squat machine"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"cuadriceps"}, Secondary: []string{"gluteo"},
		Wger: 1521,
	},
	{
		// «Pressa stretta» (pies juntos) se apunta aquí también.
		Key: "leg_press", Name: "Prensa de piernas",
		Aliases: []string{"leg press", "pressa", "pressa stretta", "pressa 45", "pressa gambe", "leg press 45", "prensa", "prensa inclinada"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores", "isquios"},
		Wger: 371, Fedb: "Leg_Press",
	},
	{
		Key: "single_leg_press", Name: "Prensa a una pierna",
		Aliases: []string{"single leg press", "single leg leg press", "prensa a una pierna", "prensa unilateral", "pressa monopodalica"},
		Kind:    "reps", Equipment: "maquina", Step: 5, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"},
		Wger:    2677,
	},
	{
		Key: "leg_extension", Name: "Extensión de cuádriceps",
		Aliases: []string{"leg extension", "leg extensions", "quad extension", "extension de cuadriceps", "extensiones de cuadriceps", "extension de piernas", "cuadriceps maquina", "leg extension macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"cuadriceps"},
		Wger:    369, Fedb: "Leg_Extensions",
	},
	{
		Key: "bulgarian_split_squat", Name: "Sentadilla búlgara",
		Aliases: []string{"bulgarian", "bulgaria", "bulgarian split", "bulgarian split squat", "bulgarian split squats", "bulgarian squat", "bulgara", "bulgaras", "sentadillas bulgaras", "affondi bulgari", "rfess"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores", "isquios"},
		Wger: 1706, Fedb: "Split_Squat_with_Dumbbells",
	},
	{
		Key: "smith_split_squat", Name: "Zancada o búlgara en multipower",
		Aliases: []string{"smith split squat", "smith lunge", "zancada multipower", "bulgara multipower", "bulgarian multipower"},
		Kind:    "reps", Equipment: "multipower", Step: 2.5,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores"},
		Wger: 1593, Fedb: "Smith_Single-Leg_Split_Squat",
	},
	{
		Key: "split_squat", Name: "Sentadilla split (zancada estática)",
		Aliases: []string{"split squat", "split squats", "static lunge", "zancada estatica", "sentadilla split"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores"},
		Wger: 1366,
	},
	{
		Key: "walking_lunge", Name: "Zancadas caminando",
		Aliases: []string{"walking lunges", "walking lunge", "lunges", "lunge", "zancadas", "zancada", "zancadas andando", "affondi", "affondi camminati"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores", "isquios"},
		Wger: 206,
	},
	{
		Key: "reverse_lunge", Name: "Zancada hacia atrás",
		Aliases: []string{"reverse lunge", "reverse lunges", "zancada atras", "zancada inversa", "affondi indietro"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"isquios"},
		Wger: 1651, Fedb: "Dumbbell_Rear_Lunge",
	},
	{
		Key: "lateral_lunge", Name: "Zancada lateral",
		Aliases: []string{"lateral lunge", "side lunge", "affondo laterale"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"cuadriceps", "aductores"}, Secondary: []string{"gluteo"},
		Wger: 1653,
	},
	{
		Key: "cossack_squat", Name: "Sentadilla cosaca",
		Aliases: []string{"cossack squat", "cossack squats", "cossack"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2, Bodyweight: true,
		Muscles: []string{"aductores", "cuadriceps"}, Secondary: []string{"gluteo"},
		Wger: 1407,
	},
	{
		Key: "step_up", Name: "Subida al cajón (step up)",
		Aliases: []string{"step up", "step ups", "stepup", "subida al cajon", "subidas al cajon", "subida a banco", "step up manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"isquios"},
		Fedb: "Dumbbell_Step_Ups",
	},
	{
		Key: "pistol_squat", Name: "Sentadilla a una pierna (pistol)",
		Aliases: []string{"pistol squat", "pistol squats", "pistol", "sentadilla pistol"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"abdomen"},
		Wger: 456,
	},
	{
		Key: "bodyweight_squat", Name: "Sentadilla sin peso",
		Aliases: []string{"air squat", "air squats", "bodyweight squat", "sentadilla libre"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"cuadriceps", "gluteo"},
		Wger:    1312, Fedb: "Bodyweight_Squat",
	},
	{
		Key: "squat_hold", Name: "Sentadilla isométrica",
		Aliases: []string{"squat hold", "isometric squat", "squat isometrico", "isometria sentadilla", "deep squat hold"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"cuadriceps", "gluteo"}, Secondary: []string{"aductores"},
		Wger: 1733,
	},
	{
		Key: "wall_sit", Name: "Sentadilla isométrica en pared",
		Aliases: []string{"wall sit", "wall squat", "sentadilla en pared", "silla", "la silla", "sedia"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"cuadriceps"}, Secondary: []string{"gluteo"},
		Wger: 718,
	},

	// --- Isquios ---
	{
		Key: "lying_leg_curl", Name: "Curl femoral tumbado",
		Aliases: []string{"leg curl", "leg curls", "lying leg curl", "hamstring curl", "curl femoral", "femoral tumbado", "curl femorale", "leg curl sdraiato"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"isquios"}, Secondary: []string{"gemelo"},
		Wger: 365, Fedb: "Lying_Leg_Curls",
	},
	{
		Key: "seated_leg_curl", Name: "Curl femoral sentado",
		Aliases: []string{"seated leg curl", "seated hamstring curl", "femoral sentado", "leg curl seduto"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"isquios"},
		Wger:    366, Fedb: "Seated_Leg_Curl",
	},
	{
		Key: "nordic_curl", Name: "Curl nórdico",
		Aliases: []string{"nordic curl", "nordic curls", "nordic hamstring curl", "nordic", "nordicos"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"isquios"},
		Wger:    910, Fedb: "Natural_Glute_Ham_Raise",
	},
	{
		Key: "glute_ham_raise", Name: "Glute ham raise",
		Aliases: []string{"glute ham raise", "ghr", "glute ham"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"isquios"}, Secondary: []string{"gluteo"},
		Wger: 2478, Fedb: "Glute_Ham_Raise",
	},

	// --- Gemelo y tibial ---
	{
		Key: "standing_calf_raise", Name: "Elevación de gemelos de pie",
		Aliases: []string{"standing calf raise", "standing calf", "calf raise", "calf raises", "calf machine", "gemelos", "gemelos de pie", "elevacion de gemelos", "elevaciones de talones", "polpacci", "polpacci in piedi"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"gemelo"},
		Wger:    622, Fedb: "Standing_Calf_Raises",
	},
	{
		// «Calf» a secas es el de sentado, que es el que hace la persona.
		Key: "seated_calf_raise", Name: "Elevación de gemelos sentado",
		Aliases: []string{"seated calf", "seat calf", "calf", "calves", "seated calf raise", "seated calf raises", "gemelos sentado", "soleo", "polpacci seduto", "calf seduto"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"gemelo"},
		Wger:    590, Fedb: "Seated_Calf_Raise",
	},
	{
		Key: "single_leg_calf_raise", Name: "Elevación de gemelo a una pierna",
		Aliases: []string{"single leg calf raise", "gemelo a una pierna", "calf raise a una pierna"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2, Bodyweight: true,
		Muscles: []string{"gemelo"},
		Wger:    702,
	},
	{
		Key: "tibialis_raise", Name: "Elevación de tibial",
		Aliases: []string{"tibialis raise", "tibialis raises", "tib raise", "tib raises", "tibial raise", "tibiales", "elevacion de puntas"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2, Bodyweight: true,
		Muscles: []string{"tibial"},
		Wger:    1200,
	},

	// --- Hombro ---
	{
		Key: "overhead_press", Name: "Press militar con barra",
		Aliases: []string{"military", "military press", "overhead press", "ohp", "barbell overhead press", "standing press", "press militar", "press militar con barra", "press de hombros con barra", "lento avanti", "military press bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps", "trapecio", "abdomen"},
		Wger: 1893, Fedb: "Standing_Military_Press",
	},
	{
		Key: "dumbbell_shoulder_press", Name: "Press de hombros con mancuernas",
		Aliases: []string{"military dumbbell", "military dumbell", "military press dumbbell", "overhead dumbell press", "overhead dumbell", "overhead dumbbell", "overhead dumbbell press", "dumbbell shoulder press", "db shoulder press", "seated dumbbell press", "dumbbell overhead press", "press militar mancuernas", "press militar con mancuernas", "press de hombros con mancuernas", "press hombro mancuernas", "lento avanti manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps", "trapecio"},
		Wger: 567, Fedb: "Dumbbell_Shoulder_Press",
	},
	{
		Key: "arnold_press", Name: "Press Arnold",
		Aliases: []string{"arnold press", "arnold"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps"},
		Wger: 20, Fedb: "Arnold_Dumbbell_Press",
	},
	{
		Key: "machine_shoulder_press", Name: "Press de hombros en máquina",
		Aliases: []string{"shoulder press", "machine shoulder press", "shoulder press machine", "press de hombros maquina", "press hombro maquina", "shoulder press macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps"},
		Wger: 543, Fedb: "Machine_Shoulder_Military_Press",
	},
	{
		Key: "smith_shoulder_press", Name: "Press militar en multipower",
		Aliases: []string{"smith shoulder press", "smith overhead press", "military multipower", "press militar multipower", "lento avanti multipower"},
		Kind:    "reps", Equipment: "multipower", Step: 2.5,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps"},
		Wger: 569, Fedb: "Smith_Machine_Overhead_Shoulder_Press",
	},
	{
		Key: "landmine_press", Name: "Press landmine",
		Aliases: []string{"landmine press", "landmine", "press con mina"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"hombro", "pecho"}, Secondary: []string{"triceps", "abdomen"},
		Wger: 346,
	},
	{
		Key: "lateral_raise", Name: "Elevaciones laterales con mancuernas",
		Aliases: []string{"lateral raise", "lateral raises", "lateral", "laterales", "elevaciones laterales", "elevacion lateral", "side raise", "side raises", "dumbbell lateral raise", "db lateral raise", "alzate laterali", "alzate laterali manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio"},
		Wger: 348, Fedb: "Side_Lateral_Raise",
	},
	{
		Key: "cable_lateral_raise", Name: "Elevación lateral en polea",
		Aliases: []string{"cable lateral raise", "cable lateral raises", "cable lateral", "lateral polea", "elevacion lateral en polea", "elevaciones laterales en polea", "alzate laterali al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"hombro"},
		Wger:    1378, Fedb: "Standing_Low-Pulley_Deltoid_Raise",
	},
	{
		Key: "machine_lateral_raise", Name: "Elevaciones laterales en máquina",
		Aliases: []string{"machine lateral raise", "lateral raise machine", "laterales en maquina", "elevaciones laterales maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"hombro"},
		Wger:    1744,
	},
	{
		Key: "front_raise", Name: "Elevaciones frontales",
		Aliases: []string{"front raise", "front raises", "elevacion frontal", "alzate frontali"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"},
		Wger:    256, Fedb: "Front_Dumbbell_Raise",
	},
	{
		Key: "rear_delt_fly", Name: "Pájaros con mancuernas (deltoides posterior)",
		Aliases: []string{"rear delt fly", "rear delt raise", "reverse fly", "rear fly", "bent over lateral raise", "pajaros", "pajaro", "aperturas invertidas", "alzate posteriori"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio", "espalda"},
		Wger: 487, Fedb: "Seated_Bent-Over_Rear_Delt_Raise",
	},
	{
		Key: "reverse_pec_deck", Name: "Contractor inverso (deltoides posterior)",
		Aliases: []string{"reverse pec deck", "rear delt machine", "reverse fly machine", "rear delt fly machine", "pec deck inverso", "contractor inverso", "pajaros en maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio", "espalda"},
		Wger: 1775, Fedb: "Reverse_Machine_Flyes",
	},
	{
		Key: "upright_row", Name: "Remo al mentón",
		Aliases: []string{"upright row", "upright rows", "remo al menton", "remo vertical", "tirate al mento"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"hombro", "trapecio"}, Secondary: []string{"biceps"},
		Wger: 693, Fedb: "Upright_Barbell_Row",
	},
	{
		Key: "band_pull_apart", Name: "Separación de banda (pull apart)",
		Aliases: []string{"band pull apart", "band pull aparts", "pull apart", "pull aparts", "aperturas con banda", "aperturas con goma"},
		Kind:    "reps", Equipment: "banda", Bodyweight: true,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio", "espalda"},
		Wger: 1380, Fedb: "Band_Pull_Apart",
	},
	{
		Key: "cable_external_rotation", Name: "Rotación externa en polea",
		Aliases: []string{"external rotation", "external rotations", "cable external rotation", "rotacion externa", "rotaciones externas", "rotazione esterna"},
		Kind:    "reps", Equipment: "polea", Step: 2.5, PerSide: true,
		Muscles: []string{"hombro"},
		Wger:    1729, Fedb: "External_Rotation_with_Cable",
	},
	{
		Key: "y_raise", Name: "Elevaciones en Y",
		Aliases: []string{"y raise", "y raises", "incline y raise", "elevaciones en y"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"trapecio"},
		Wger: 1879,
	},
	{
		Key: "scapular_push_up", Name: "Flexión escapular",
		Aliases: []string{"scapular push", "scapular push up", "scapular push ups", "scapular pushup", "scap push up", "flexiones escapulares", "protraccion escapular"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"hombro"}, Secondary: []string{"pecho"},
		Wger: 2681,
	},
	{
		Key: "pike_push_up", Name: "Flexiones en pica",
		Aliases: []string{"pike push up", "pike push ups", "pike pushup", "flexiones en pica"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps"},
		Wger: 454,
	},

	// --- Trapecio ---
	{
		Key: "barbell_shrug", Name: "Encogimientos con barra",
		Aliases: []string{"barbell shrug", "barbell shrugs", "shrug bilanciere", "scrollate bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"trapecio"}, Secondary: []string{"antebrazo"},
		Wger: 571, Fedb: "Barbell_Shrug",
	},
	{
		Key: "dumbbell_shrug", Name: "Encogimientos con mancuernas",
		Aliases: []string{"shrug", "shrugs", "dumbbell shrug", "dumbbell shrugs", "encogimientos", "encogimientos de hombros", "scrollate", "scrollate manubri"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"trapecio"}, Secondary: []string{"antebrazo"},
		Wger: 572, Fedb: "Dumbbell_Shrug",
	},

	// --- Bíceps ---
	{
		// «Curl» a secas es el de mancuernas.
		Key: "dumbbell_curl", Name: "Curl de bíceps con mancuernas",
		Aliases: []string{"curl", "curls", "dumbell curls", "dumbbell curls", "dumbell curl", "dumbbell curl", "bicep curl", "bicep curls", "biceps curl", "biceps curls", "db curl", "alternate curl", "curl biceps", "curl de biceps", "curl mancuernas", "curl alterno", "biceps", "curl manubri", "bicipiti"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"biceps"}, Secondary: []string{"antebrazo"},
		Wger: 92, Fedb: "Dumbbell_Bicep_Curl",
	},
	{
		Key: "barbell_curl", Name: "Curl con barra",
		Aliases: []string{"barbell curl", "barbell curls", "bb curl", "curl barra", "curl bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"biceps"}, Secondary: []string{"antebrazo"},
		Wger: 91, Fedb: "Barbell_Curl",
	},
	{
		Key: "ez_bar_curl", Name: "Curl con barra Z",
		Aliases: []string{"ez bar curl", "ez curl", "curl barra z", "curl con barra z", "curl barra ez"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"biceps"}, Secondary: []string{"antebrazo"},
		Wger: 94, Fedb: "EZ-Bar_Curl",
	},
	{
		Key: "hammer_curl", Name: "Curl martillo",
		Aliases: []string{"hammer curl", "hammer curls", "hammer", "martillo", "curl a martello"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"biceps", "antebrazo"},
		Wger:    272, Fedb: "Hammer_Curls",
	},
	{
		Key: "incline_dumbbell_curl", Name: "Curl inclinado con mancuernas",
		Aliases: []string{"incline curl", "incline curls", "incline dumbbell curl", "incline dumbell curl", "curl inclinado", "curl su panca inclinata"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"biceps"},
		Wger:    204, Fedb: "Incline_Dumbbell_Curl",
	},
	{
		Key: "preacher_curl", Name: "Curl en banco Scott",
		Aliases: []string{"preacher curl", "preacher curls", "scott curl", "curl scott", "curl predicador", "panca scott"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"biceps"},
		Wger:    465, Fedb: "Preacher_Curl",
	},
	{
		Key: "machine_curl", Name: "Curl de bíceps en máquina",
		Aliases: []string{"machine curl", "biceps machine", "bicep machine", "machine preacher curl", "curl en maquina", "curl maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"biceps"},
		Wger:    1424, Fedb: "Machine_Bicep_Curl",
	},
	{
		Key: "cable_curl", Name: "Curl en polea",
		Aliases: []string{"cable curl", "cable curls", "curl polea", "curl al cavo", "bayesian curl"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"biceps"}, Secondary: []string{"antebrazo"},
		Wger: 95, Fedb: "Standing_Biceps_Cable_Curl",
	},
	{
		Key: "concentration_curl", Name: "Curl concentrado",
		Aliases: []string{"concentration curl", "concentration curls", "curl di concentrazione"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"biceps"},
		Wger:    202, Fedb: "Concentration_Curls",
	},
	{
		Key: "reverse_curl", Name: "Curl inverso (agarre prono)",
		Aliases: []string{"reverse curl", "reverse curls", "curl prono", "curl invertito"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"antebrazo", "biceps"},
		Wger:    1290, Fedb: "Reverse_Barbell_Curl",
	},

	// --- Tríceps ---
	{
		Key: "triceps_pushdown", Name: "Jalón de tríceps en polea (pushdown)",
		Aliases: []string{"triceps pushdown", "tricep pushdown", "pushdown", "pushdowns", "push down", "rope pushdown", "cable pushdown", "triceps rope", "triceps polea", "jalon de triceps", "pushdown al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"triceps"},
		Wger:    805, Fedb: "Triceps_Pushdown_-_Rope_Attachment",
	},
	{
		Key: "overhead_cable_triceps_extension", Name: "Extensión de tríceps sobre la cabeza en polea",
		Aliases: []string{"tríceps extensión", "triceps extension", "triceps extensions", "tricep extension", "overhead triceps extension", "overhead tricep extension", "overhead cable extension", "extension de triceps", "extensiones de triceps", "french press al cavo", "estensioni tricipiti"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"triceps"},
		Wger:    1513, Fedb: "Triceps_Overhead_Extension_with_Rope",
	},
	{
		Key: "dumbbell_triceps_extension", Name: "Extensión de tríceps tras nuca con mancuerna",
		Aliases: []string{"dumbbell triceps extension", "dumbbell tricep extension", "dumbbell overhead extension", "overhead dumbbell extension", "extension de triceps tras nuca", "tras nuca", "french press manubrio"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"triceps"},
		Wger:    211, Fedb: "Standing_Dumbbell_Triceps_Extension",
	},
	{
		Key: "skull_crusher", Name: "Press francés (rompecráneos)",
		Aliases: []string{"skull crusher", "skull crushers", "skullcrusher", "skullcrushers", "lying triceps extension", "french press", "press frances", "rompecraneos", "french press bilanciere"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"triceps"},
		Wger:    246, Fedb: "EZ-Bar_Skullcrusher",
	},
	{
		Key: "close_grip_bench_press", Name: "Press de banca agarre cerrado",
		Aliases: []string{"close grip bench press", "close grip bench", "cgbp", "press banca agarre cerrado", "press cerrado", "panca presa stretta"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"triceps", "pecho"}, Secondary: []string{"hombro"},
		Wger: 76, Fedb: "Close-Grip_Barbell_Bench_Press",
	},
	{
		Key: "bench_dip", Name: "Fondos en banco",
		Aliases: []string{"bench dip", "bench dips", "fondos de triceps", "dip su panca"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"triceps"}, Secondary: []string{"hombro", "pecho"},
		Wger: 197, Fedb: "Bench_Dips",
	},
	{
		Key: "triceps_kickback", Name: "Patada de tríceps",
		Aliases: []string{"triceps kickback", "tricep kickback", "kickback", "kickbacks", "patada de triceps", "kickback manubrio"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"triceps"},
		Wger:    655, Fedb: "Tricep_Dumbbell_Kickback",
	},
	{
		Key: "diamond_push_up", Name: "Flexiones diamante",
		Aliases: []string{"diamond push up", "diamond push ups", "close grip push up", "flexiones cerradas"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"triceps", "pecho"}, Secondary: []string{"hombro"},
		Wger: 386, Fedb: "Push-Ups_-_Close_Triceps_Position",
	},
	{
		Key: "triceps_machine", Name: "Fondos de tríceps en máquina",
		Aliases: []string{"triceps machine", "tricep machine", "dip machine", "seated dip machine", "maquina de triceps", "fondos en maquina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"triceps"}, Secondary: []string{"pecho"},
		Fedb: "Dip_Machine",
	},

	// --- Antebrazo y agarre ---
	{
		Key: "wrist_curl", Name: "Curl de muñeca",
		Aliases: []string{"wrist curl", "wrist curls", "curl de muneca", "flexion de muneca"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"antebrazo"},
		Wger:    1205, Fedb: "Seated_Dumbbell_Palms-Up_Wrist_Curl",
	},
	{
		Key: "reverse_wrist_curl", Name: "Curl de muñeca inverso",
		Aliases: []string{"reverse wrist curl", "wrist extension", "curl de muneca inverso", "extension de muneca"},
		Kind:    "reps", Equipment: "mancuerna", Step: 2,
		Muscles: []string{"antebrazo"},
		Wger:    1881, Fedb: "Seated_Dumbbell_Palms-Down_Wrist_Curl",
	},
	{
		Key: "dead_hang", Name: "Colgarse de la barra (dead hang)",
		Aliases: []string{"dead hang", "hang", "colgarse", "colgado en barra", "sospensione alla sbarra"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"antebrazo"}, Secondary: []string{"espalda", "hombro"},
	},

	// --- Acarreos y trineo (metros) ---
	{
		Key: "farmer_carry", Name: "Paseo del granjero",
		Aliases: []string{"farmers carry", "farmer carry", "farmer's carry", "farmers walk", "farmer walk", "farmer", "paseo granjero", "camminata del contadino"},
		Kind:    "distance", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"antebrazo", "trapecio"}, Secondary: []string{"abdomen", "cuerpo entero"},
		Wger: 1116, Fedb: "Farmers_Walk",
	},
	{
		Key: "suitcase_carry", Name: "Paseo de maleta (a una mano)",
		Aliases: []string{"suitcase carry", "suitcase walk", "paseo de maleta", "maleta"},
		Kind:    "distance", Equipment: "mancuerna", Step: 2, PerSide: true,
		Muscles: []string{"abdomen", "antebrazo"}, Secondary: []string{"trapecio"},
		Wger: 1776,
	},
	{
		Key: "overhead_carry", Name: "Paseo con peso sobre la cabeza",
		Aliases: []string{"overhead carry", "waiter carry", "waiters walk", "waiter walk", "paseo de camarero"},
		Kind:    "distance", Equipment: "kettlebell", Step: 4, PerSide: true,
		Muscles: []string{"hombro", "abdomen"}, Secondary: []string{"trapecio"},
	},
	{
		Key: "sled_push", Name: "Empuje de trineo",
		Aliases: []string{"sled push", "sled", "prowler", "trineo", "spinta slitta"},
		Kind:    "distance", Equipment: "otro", Step: 10,
		Muscles: []string{"cuadriceps", "cuerpo entero"}, Secondary: []string{"gluteo", "gemelo"},
		Wger: 1523, Fedb: "Sled_Push",
	},

	// --- Core ---
	{
		Key: "plank", Name: "Plancha abdominal",
		Aliases: []string{"plank", "planks", "front plank", "plancha", "plancha frontal", "plank frontale"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"hombro"},
		Wger: 458, Fedb: "Plank",
	},
	{
		Key: "side_plank", Name: "Plancha lateral",
		Aliases: []string{"side plank", "side planks", "lateral plank", "plank laterale"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"gluteo", "hombro"},
		Wger: 580, Fedb: "Side_Bridge",
	},
	{
		Key: "hollow_hold", Name: "Hollow hold",
		Aliases: []string{"hollow hold", "hollow", "hollow body", "hollow body hold", "posicion hueca"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    297,
	},
	{
		Key: "l_sit", Name: "L-sit",
		Aliases: []string{"l sit", "lsit", "l sit hold"},
		Kind:    "time", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"triceps", "cuadriceps"},
		Wger: 1852,
	},
	{
		Key: "dead_bug", Name: "Dead bug",
		Aliases: []string{"deadbug", "dead bug", "dead bugs", "deadbugs", "bicho muerto"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    178, Fedb: "Dead_Bug",
	},
	{
		Key: "crunch", Name: "Crunch abdominal",
		Aliases: []string{"crunch", "crunches", "abdominales", "abs", "encogimientos abdominales", "crunch a terra"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    167, Fedb: "Crunches",
	},
	{
		Key: "sit_up", Name: "Sit-up (abdominal completo)",
		Aliases: []string{"situp", "situps", "sit up", "sit ups", "abdominales completos"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Fedb:    "Sit-Up",
	},
	{
		// «Addome» a secas es el sit-up en banco inclinado de la persona.
		Key: "decline_sit_up", Name: "Sit-up en banco inclinado",
		Aliases: []string{"addome", "addome situp inclinado", "situp inclinado", "sit up inclinado", "decline sit up", "decline situp", "incline sit up", "incline situp", "abdominales en banco", "abdominales en banco inclinado", "abdominales en banco declinado", "addome panca inclinata", "decline crunch"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2.5, Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    171,
	},
	{
		Key: "abdominal_machine", Name: "Crunch en máquina abdominal",
		Aliases: []string{"abdominal machine", "ab machine", "ab crunch machine", "abdominal crunch machine", "crunch machine", "maquina de abdominales", "maquina abdominal", "crunch en maquina", "addome macchina"},
		Kind:    "reps", Equipment: "maquina", Step: 5,
		Muscles: []string{"abdomen"},
		Wger:    172, Fedb: "Ab_Crunch_Machine",
	},
	{
		Key: "cable_crunch", Name: "Crunch en polea alta",
		Aliases: []string{"cable crunch", "cable crunches", "kneeling cable crunch", "crunch en polea", "crunch polea", "crunch al cavo"},
		Kind:    "reps", Equipment: "polea", Step: 5,
		Muscles: []string{"abdomen"},
		Wger:    173, Fedb: "Cable_Crunch",
	},
	{
		Key: "hanging_leg_raise", Name: "Elevación de piernas colgado",
		Aliases: []string{"hanging leg raise", "hanging leg raises", "hanging knee raise", "knee raise", "knee raises", "toes to bar", "leg raise colgado", "elevaciones de piernas colgado", "elevacion de rodillas", "sollevamento gambe alla sbarra"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"antebrazo"},
		Wger: 283, Fedb: "Hanging_Leg_Raise",
	},
	{
		Key: "captains_chair", Name: "Elevación de piernas en paralelas (silla romana)",
		Aliases: []string{"captains chair", "captain chair", "captain's chair", "roman chair leg raise", "silla romana", "elevaciones en paralelas"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    2674, Fedb: "Knee_Hip_Raise_On_Parallel_Bars",
	},
	{
		Key: "lying_leg_raise", Name: "Elevación de piernas tumbado",
		Aliases: []string{"leg raise", "leg raises", "lying leg raise", "lying leg raises", "elevacion de piernas", "elevaciones de piernas", "sollevamento gambe"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    377, Fedb: "Flat_Bench_Lying_Leg_Raise",
	},
	{
		Key: "reverse_crunch", Name: "Crunch inverso",
		Aliases: []string{"reverse crunch", "reverse crunches", "crunch invertido", "crunch inverso a terra"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    1772, Fedb: "Reverse_Crunch",
	},
	{
		Key: "russian_twist", Name: "Giro ruso",
		Aliases: []string{"russian twist", "russian twists", "giros rusos", "twist russo"},
		Kind:    "reps", Equipment: "peso corporal", Step: 2, Bodyweight: true,
		Muscles: []string{"abdomen"},
		Wger:    1193, Fedb: "Russian_Twist",
	},
	{
		Key: "mountain_climber", Name: "Escaladores",
		Aliases: []string{"mountain climber", "mountain climbers", "escalador", "scalatori"},
		Kind:    "reps", Equipment: "peso corporal", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"hombro", "cuadriceps"},
		Wger: 996, Fedb: "Mountain_Climbers",
	},
	{
		Key: "ab_wheel", Name: "Rueda abdominal",
		Aliases: []string{"ab wheel", "ab rollout", "ab roller", "rollout", "rollouts", "rueda", "ruota addominale"},
		Kind:    "reps", Equipment: "otro", Bodyweight: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"hombro", "espalda"},
		Wger: 1573, Fedb: "Ab_Roller",
	},
	{
		Key: "pallof_press", Name: "Press Pallof",
		Aliases: []string{"pallof press", "pallof", "anti rotation press", "antirotacion"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"abdomen"},
		Wger:    1194, Fedb: "Pallof_Press",
	},
	{
		Key: "cable_woodchop", Name: "Leñador en polea",
		Aliases: []string{"woodchopper", "wood chopper", "wood chop", "woodchop", "cable woodchop", "leñador", "lenador"},
		Kind:    "reps", Equipment: "polea", Step: 5, PerSide: true,
		Muscles: []string{"abdomen"}, Secondary: []string{"hombro"},
		Wger: 145, Fedb: "Standing_Cable_Wood_Chop",
	},

	// --- Kettlebell y cuerpo entero ---
	{
		Key: "turkish_get_up", Name: "Levantamiento turco (turkish get-up)",
		Aliases: []string{"turkish get up", "turkish getup", "tgu", "get up", "levantamiento turco"},
		Kind:    "reps", Equipment: "kettlebell", Step: 4, PerSide: true,
		Muscles: []string{"cuerpo entero"}, Secondary: []string{"hombro", "abdomen"},
		Fedb: "Kettlebell_Turkish_Get-Up_Lunge_style",
	},
	{
		Key: "kettlebell_clean", Name: "Cargada con kettlebell",
		Aliases: []string{"kettlebell clean", "kb clean", "cargada con kettlebell"},
		Kind:    "reps", Equipment: "kettlebell", Step: 4, PerSide: true,
		Muscles: []string{"cuerpo entero"}, Secondary: []string{"gluteo", "hombro"},
		Fedb: "One-Arm_Kettlebell_Clean",
	},
	{
		Key: "kettlebell_press", Name: "Press de hombro con kettlebell",
		Aliases: []string{"kettlebell press", "kb press", "press con kettlebell", "press kettlebell"},
		Kind:    "reps", Equipment: "kettlebell", Step: 4, PerSide: true,
		Muscles: []string{"hombro"}, Secondary: []string{"triceps", "abdomen"},
	},
	{
		Key: "power_clean", Name: "Cargada de potencia",
		Aliases: []string{"power clean", "clean", "cargada", "girata"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"cuerpo entero"}, Secondary: []string{"trapecio", "gluteo", "cuadriceps"},
		Wger: 683, Fedb: "Power_Clean",
	},
	{
		Key: "thruster", Name: "Thruster",
		Aliases: []string{"thruster", "thrusters"},
		Kind:    "reps", Equipment: "barra", Step: 2.5,
		Muscles: []string{"cuerpo entero"}, Secondary: []string{"cuadriceps", "hombro"},
		Wger: 650,
	},
}

// byKey devuelve el ejercicio de una clave.
func byKey(key string) (exercise, bool) {
	for _, e := range catalog {
		if e.Key == key {
			return e, true
		}
	}
	return exercise{}, false
}
