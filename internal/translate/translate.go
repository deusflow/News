package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

// mockTranslations has demo translations (only Ukrainian)
var mockTranslations = map[string]map[string]string{
	"uk": {
		"Blodig tendens: Samråd venter om kvindedrab":      "Кривава тенденція: Очікуються консультації щодо вбивств жінок",
		"Ukrainske våben skal produceres i Danmark":        "Українська зброя буде вироблятися в Данії",
		"Her er Danmarks hold til VM i atletik":            "Ось склад збірної Данії з легкої атлетики",
		"Putin afviser planer om at ville angribe Europa":  "Путін відкидає плани щодо нападу на Європу",
		"Gavmild ejendomsgigant":                           "Щедрий гігант нерухомості",
		"Siden nytår er 15 kvinder blevet dræbt i Danmark": "З Нового року в Данії було вбито 15 жінок. Вчора це знову сталося в Оденсе. Сьогодні міністр юстиції має відповісти, як зупинити цю тенденцію.",
		"Ifølge en mail, DR er kommet i besiddelse af":     "Згідно з листом, який отримав DR, в Данії буде вироблятися паливо для українських ракет, і в листі також описується, де це відбуватиметься",
		"Danmark sender ni atleter til VM i atletik":       "Данія відправляє дев'ять атлетів на чемпіонат світу з легкої атлетики",
		"Socialdemokratiets overborgmesterkandidat":        "Кандидат у мери від Соціал-демократичної партії Перніле Розенкранц-Тайль отримала розкішні великі приміщення безкоштовно від відомої девелоперської компанії для своєї виборчої кампанії. Вона заперечує, що щось їм винна",
	},
}

// TranslateText translates text with best available service
func TranslateText(text, from, to string) (string, error) {
	// If text is empty, return as is
	if text == "" {
		return text, nil
	}

	// Only support translate to Ukrainian
	if to != "uk" && to != "ukrainian" {
		return text, nil
	}

	// Clean text for translation
	text = cleanTextForTranslation(text)

	// Limit text length for API
	originalText := text
	if len(text) > 4000 {
		text = text[:4000] + "..."
	}

	// First try Google Translate (FREE!)
	result, err := translateWithGoogleTranslate(text, from, to)
	if err == nil && result != "" && result != text {
		log.Printf("Google Translate %s->%s ok", from, to)
		return result, nil
	}
	log.Printf("Google Translate not work for %s->%s: %v", from, to, err)

	// Then try OpenAI (if token is set)
	if openaiToken := os.Getenv("OPENAI_API_KEY"); openaiToken != "" {
		result, err := translateWithOpenAI(text, from, to)
		if err == nil && result != "" && result != text {
			log.Printf("OpenAI translate %s->%s ok", from, to)
			return result, nil
		}
		log.Printf("OpenAI not work for %s->%s: %v", from, to, err)
	}

	// Then try LibreTranslate
	result, err = translateWithLibreTranslate(text, from, to)
	if err == nil && result != "" && result != text {
		log.Printf("LibreTranslate %s->%s ok", from, to)
		return result, nil
	}

	log.Printf("All translate services not work for %s->%s, use original", from, to)
	return originalText, nil
}

// translateWithGoogleTranslate uses FREE Google Translate API
func translateWithGoogleTranslate(text, from, to string) (string, error) {
	// Use public Google Translate endpoint (free)
	baseURL := "https://translate.googleapis.com/translate_a/single"

	// Make request params
	params := url.Values{}
	params.Set("client", "gtx")
	params.Set("sl", "da") // source language: Danish
	params.Set("tl", "uk") // target language: Ukrainian
	params.Set("dt", "t")  // return translations
	params.Set("q", text)

	fullURL := baseURL + "?" + params.Encode()

	// Make HTTP client with timeout
	client := &http.Client{Timeout: 15 * time.Second}

	// Dela
