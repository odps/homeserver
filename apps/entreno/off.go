package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Open Food Facts (https://world.openfoodfacts.org, datos ODbL): valores de
// productos envasados por código de barras o por nombre. Solo se consulta
// cuando alguien lo pide en la web; lo único que sale es el código o el
// texto buscado (sin la cuenta ni nada más).

const (
	offHost      = "world.openfoodfacts.org"
	offUserAgent = "homeserver-personal/1.0 (uso personal)"
	offMaxBody   = 4 << 20
	offFields    = "code,product_name,product_name_es,brands,nutriments,serving_quantity,quantity"
)

var reBarcode = regexp.MustCompile(`^[0-9]{8,14}$`)

// offProduct es un producto con sus valores por 100 g (o ml).
type offProduct struct {
	Code    string
	Name    string
	Brand   string
	Per100  macros
	Serving float64 // gramos de una ración, si viene
	Liquid  bool
}

func (p offProduct) Title() string {
	if p.Brand != "" {
		return p.Name + " (" + p.Brand + ")"
	}
	return p.Name
}

// food es el producto como habitual.
func (p offProduct) food() food {
	f := food{Name: p.Title(), Base: baseG, Per: p.Per100.rounded(), Portion: 100, Origin: "off:" + p.Code}
	if p.Liquid {
		f.Base = baseML
	}
	if p.Serving > 0 && p.Serving <= 2000 {
		f.Portion = p.Serving
	}
	return f
}

type offClient struct {
	http *http.Client
	base string // https://world.openfoodfacts.org (en los tests, otro)

	mu   sync.Mutex
	last time.Time // última búsqueda por nombre (la API pide no más de 10 por minuto)
}

func newOFF() *offClient {
	return &offClient{http: &http.Client{Timeout: 15 * time.Second}, base: "https://" + offHost}
}

var errOFFBusy = errors.New("Open Food Facts limita las búsquedas: espera unos segundos")

type offRaw struct {
	Code       string         `json:"code"`
	Name       string         `json:"product_name"`
	NameES     string         `json:"product_name_es"`
	Brands     string         `json:"brands"`
	Nutriments map[string]any `json:"nutriments"`
	Serving    any            `json:"serving_quantity"`
	Quantity   string         `json:"quantity"`
}

func offNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(strings.Replace(x, ",", ".", 1), "%g", &f)
		return f
	}
	return 0
}

func (r offRaw) product() (offProduct, bool) {
	p := offProduct{Code: r.Code, Name: strings.TrimSpace(r.NameES), Brand: strings.TrimSpace(strings.Split(r.Brands, ",")[0])}
	if p.Name == "" {
		p.Name = strings.TrimSpace(r.Name)
	}
	n := r.Nutriments
	p.Per100 = macros{offNum(n["energy-kcal_100g"]), offNum(n["proteins_100g"]), offNum(n["carbohydrates_100g"]), offNum(n["fat_100g"])}
	if p.Per100.Kcal == 0 {
		if kj := offNum(n["energy_100g"]); kj > 0 {
			p.Per100.Kcal = kj / 4.184
		}
	}
	p.Serving = offNum(r.Serving)
	q := strings.ToLower(r.Quantity)
	p.Liquid = strings.HasSuffix(q, "ml") || strings.HasSuffix(q, " l") || strings.HasSuffix(q, "cl")
	if len([]rune(p.Name)) > 70 {
		p.Name = string([]rune(p.Name)[:70])
	}
	if len([]rune(p.Brand)) > 30 {
		p.Brand = string([]rune(p.Brand)[:30])
	}
	ok := p.Name != "" && reBarcode.MatchString(p.Code) && p.Per100.Kcal > 0 && p.Per100.Kcal <= 950 && p.Per100.check() == nil
	return p, ok
}

func (c *offClient) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", offUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("Open Food Facts no responde")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return errOFFBusy
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Open Food Facts respondió %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, offMaxBody))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// barcode busca un producto por su código de barras.
func (c *offClient) barcode(ctx context.Context, code string) (offProduct, error) {
	code = strings.TrimSpace(code)
	if !reBarcode.MatchString(code) {
		return offProduct{}, errors.New("el código de barras son de 8 a 14 cifras")
	}
	var res struct {
		Status  int    `json:"status"`
		Product offRaw `json:"product"`
	}
	if err := c.get(ctx, "/api/v2/product/"+code+".json", url.Values{"fields": {offFields}}, &res); err != nil {
		if errors.Is(err, errNotFound) {
			return offProduct{}, errors.New("ese código no está en Open Food Facts")
		}
		return offProduct{}, err
	}
	res.Product.Code = code
	p, ok := res.Product.product()
	if res.Status != 1 || !ok {
		return offProduct{}, errors.New("ese código no está en Open Food Facts o no tiene los valores nutricionales")
	}
	return p, nil
}

// search busca productos por nombre (los que tienen valores).
func (c *offClient) search(ctx context.Context, text string) ([]offProduct, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, errors.New("escribe qué buscar")
	}
	if len([]rune(text)) > 80 {
		text = string([]rune(text)[:80])
	}
	c.mu.Lock()
	if time.Since(c.last) < 6*time.Second {
		c.mu.Unlock()
		return nil, errOFFBusy
	}
	c.last = time.Now()
	c.mu.Unlock()
	var res struct {
		Products []offRaw `json:"products"`
	}
	q := url.Values{
		"search_terms": {text}, "search_simple": {"1"}, "action": {"process"}, "json": {"1"},
		"page_size": {"20"}, "fields": {offFields}, "lc": {"es"},
	}
	if err := c.get(ctx, "/cgi/search.pl", q, &res); err != nil {
		return nil, err
	}
	var out []offProduct
	for _, r := range res.Products {
		if p, ok := r.product(); ok {
			out = append(out, p)
		}
	}
	return out, nil
}
