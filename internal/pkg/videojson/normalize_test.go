package videojson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode normalized body: %v; body=%s", err, body)
	}
	return value
}

func TestNormalizeVideoAliasesAndEnvelope(t *testing.T) {
	body, err := Normalize([]byte(`{"code":0,"message":"ok","data":{"videoUrl":"https://cdn.example/video.mp4","description":"demo","user":{"uid":9007199254740993,"nickname":"alice","avatarUrl":"https://cdn.example/avatar.jpg"},"thumbnail":"https://cdn.example/cover.jpg","duration":12,"aweme_id":"a-1","statistics":{"like_count":4}}}`))
	if err != nil {
		t.Fatal(err)
	}
	root := decode(t, body)
	if root["code"] != float64(200) || root["msg"] != "ok" {
		t.Fatalf("unexpected envelope: %#v", root)
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %#v", root["data"])
	}
	if data["type"] != "video" || data["url"] != "https://cdn.example/video.mp4" || data["cover"] != "https://cdn.example/cover.jpg" {
		t.Fatalf("unexpected video fields: %#v", data)
	}
	author := data["author"].(map[string]any)
	if author["name"] != "alice" || author["id"] != "9007199254740993" {
		t.Fatalf("author aliases or large id were not preserved: %#v", author)
	}
	extra := data["extra"].(map[string]any)
	if extra["aweme_id"] != "a-1" || extra["statistics"] == nil {
		t.Fatalf("platform metadata was not retained: %#v", extra)
	}
	for _, key := range []string{"type", "title", "desc", "author", "cover", "url", "quality", "duration", "images", "live_photo", "music", "extra"} {
		if _, exists := data[key]; !exists {
			t.Fatalf("required field %q missing from %#v", key, data)
		}
	}
}

func TestNormalizeImageAndLivePhotoResources(t *testing.T) {
	imageBody, err := Normalize([]byte(`{"result":{"type":"images","pictures":["https://img/1.jpg","https://img/2.jpg"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	image := decode(t, imageBody)["data"].(map[string]any)
	if image["type"] != "image" || image["url"] != nil || image["duration"] != nil {
		t.Fatalf("image scalar fields are wrong: %#v", image)
	}
	if len(image["images"].([]any)) != 2 {
		t.Fatalf("image list was not retained: %#v", image["images"])
	}

	liveBody, err := Normalize([]byte(`{"data":{"livePhotos":[{"imageUrl":"https://img/live.jpg","videoUrl":"https://video/live.mp4"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	live := decode(t, liveBody)["data"].(map[string]any)
	if live["type"] != "live" || live["url"] != nil || len(live["live_photo"].([]any)) != 1 {
		t.Fatalf("live-photo fields are wrong: %#v", live)
	}
	liveImages := live["images"].([]any)
	if len(liveImages) != 1 || liveImages[0] != "https://img/live.jpg" {
		t.Fatalf("live-photo still image was not retained: %#v", liveImages)
	}
	arrayBody, err := Normalize([]byte(`["https://img/array-1.jpg","https://img/array-2.jpg"]`))
	if err != nil {
		t.Fatal(err)
	}
	arrayData := decode(t, arrayBody)["data"].(map[string]any)
	if arrayData["type"] != "image" || len(arrayData["images"].([]any)) != 2 {
		t.Fatalf("raw image array was not normalized: %#v", arrayData)
	}

	objectArrayBody, err := Normalize([]byte(`[{"url":"https://img/object-1.jpg"},{"url":"https://img/object-2.jpg"}]`))
	if err != nil {
		t.Fatal(err)
	}
	objectArrayData := decode(t, objectArrayBody)["data"].(map[string]any)
	if objectArrayData["type"] != "image" || len(objectArrayData["images"].([]any)) != 2 {
		t.Fatalf("object image array was not normalized: %#v", objectArrayData)
	}
}

func TestNormalizePrefersNoWatermarkVideoURL(t *testing.T) {
	body, err := Normalize([]byte(`{"data":{"video_url":"https://video/watermarked.mp4","no_watermark_url":"https://video/clean.mp4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	data := decode(t, body)["data"].(map[string]any)
	if data["url"] != "https://video/clean.mp4" {
		t.Fatalf("preferred video URL = %v", data["url"])
	}

	body, err = Normalize([]byte(`{"no_watermark_url":"https://video/outer-clean.mp4","data":{"video_url":"https://video/inner-watermarked.mp4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	data = decode(t, body)["data"].(map[string]any)
	if data["url"] != "https://video/outer-clean.mp4" {
		t.Fatalf("outer preferred video URL = %v", data["url"])
	}
}

func TestNormalizeNestedStatusConventions(t *testing.T) {
	body, err := Normalize([]byte(`{"response":{"payload":{"status":1,"message":"ok","video_url":"https://video/success.mp4"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	root := decode(t, body)
	if root["code"] != float64(200) || root["msg"] != "ok" {
		t.Fatalf("nested success envelope = %#v", root)
	}

	body, err = Normalize([]byte(`{"data":{"success":false,"message":"upstream failed","data":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	root = decode(t, body)
	if root["code"] != float64(500) || root["msg"] != "upstream failed" {
		t.Fatalf("nested failure envelope = %#v", root)
	}

	body, err = Normalize([]byte(`{"code":0,"success":false,"message":"explicit failure"}`))
	if err != nil {
		t.Fatal(err)
	}
	root = decode(t, body)
	if root["code"] != float64(500) {
		t.Fatalf("success:false did not override code:0: %#v", root)
	}
}

func TestNormalizeDurationMillisecondsAndMetadata(t *testing.T) {
	body, err := Normalize([]byte(`{"data":{"video_url":"https://video/duration.mp4","duration_ms":12500}}`))
	if err != nil {
		t.Fatal(err)
	}
	data := decode(t, body)["data"].(map[string]any)
	if data["duration"] != float64(13) {
		t.Fatalf("duration = %v, want rounded seconds", data["duration"])
	}
	extra := data["extra"].(map[string]any)
	if extra["duration_ms"] != float64(12500) {
		t.Fatalf("duration_ms metadata was not preserved: %#v", extra)
	}
}

func TestNormalizePassesNonJSONAndRejectsMalformedJSON(t *testing.T) {
	text := []byte("plain text response")
	result, err := NormalizeWithResult(text)
	if err != nil || result.IsJSON || string(result.Body) != string(text) {
		t.Fatalf("non-JSON response was changed: result=%+v err=%v", result, err)
	}
	if _, err := Normalize([]byte(`{"data":`)); err == nil {
		t.Fatal("malformed JSON was accepted")
	} else if !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("malformed JSON error was not classified: %v", err)
	}
	if _, err := Normalize([]byte(`{"code":500,"message":"bad"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeAlreadyUnifiedResponseIsNotDoubleWrapped(t *testing.T) {
	body, err := Normalize([]byte(`{"code":200,"msg":"解析成功","data":{"type":"video","title":"t","desc":"d","author":{"name":"","id":"","avatar":""},"cover":"","url":"https://video/x","quality":"","duration":null,"images":[],"live_photo":[],"music":{},"extra":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	root := decode(t, body)
	data := root["data"].(map[string]any)
	if _, exists := data["data"]; exists || data["url"] != "https://video/x" {
		t.Fatalf("unified response was wrapped a second time: %#v", data)
	}
}

func TestNormalizeKeepsEmptyContractFields(t *testing.T) {
	body, err := Normalize([]byte(`{"code":200,"data":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	data := decode(t, body)["data"].(map[string]any)
	author := data["author"].(map[string]any)
	if data["type"] != "video" || data["url"] != "" || data["images"].([]any) == nil || data["live_photo"].([]any) == nil || data["music"].(map[string]any) == nil || data["extra"].(map[string]any) == nil {
		t.Fatalf("empty fields do not follow contract: %#v", data)
	}
	if author["name"] != "" || author["id"] != "" || author["avatar"] != "" {
		t.Fatalf("author empty fields do not follow contract: %#v", author)
	}
	if !strings.Contains(string(body), `"duration":null`) {
		t.Fatalf("duration null missing: %s", body)
	}
}
