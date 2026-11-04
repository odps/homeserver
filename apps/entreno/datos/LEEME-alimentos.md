# alimentos.json

Tabla básica de alimentos genéricos con nombre en español: kcal, proteína, hidratos (por diferencia), grasa y fibra por 100 g (por 100 ml si `liquid` es true; se toma 100 ml ≈ 100 g, salvo el aceite, cuya cucharada pesa 13,5 g).

- **Origen:** USDA FoodData Central, SR Legacy (abril de 2018), <https://fdc.nal.usda.gov>. Cada alimento lleva su `fdc` para consultarlo allí.
- **Licencia:** dominio público (obra del Gobierno de EE. UU., CC0). Se cita la fuente por cortesía.
- **Mapeo:** hecho a mano en `generar_alimentos.go`, con los nombres, alias, unidades y la variante por defecto. Cuando USDA no tiene el alimento español (jamón serrano, manchego, merluza, tomate frito…) se usa el más parecido y lo dice un comentario al lado.
- **Regenerar** (desde `apps/entreno`): `go run generar_alimentos.go` descarga el zip de CSV de USDA; `go run generar_alimentos.go <carpeta>` usa una copia ya descomprimida.
