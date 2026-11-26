package appstore

import (
	"errors"
	"fmt"
	gohttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/majd/ipatool/v2/pkg/http"
	"howett.net/plist"
)

// 店面发现是搜索/登录/下载的共同前置，而它只是个只读的公共查询：苹果侧抖一下
// （5xx / 429 / 连接被重置）就让「搜索」直接报错，看起来像功能坏了。这里给它
// 几次快速重试 —— 用户不必自己去点第二次。
const (
	storefrontDiscoveryAttempts = 3
	storefrontRetryDelay        = 300 * time.Millisecond
)

// storefrontRetrySleep 只为了测试里不真的等（生产就是 time.Sleep）。
var storefrontRetrySleep = time.Sleep

type availableStorefrontsResult struct {
	Storefronts []availableStorefront `plist:"storefronts"`
}

type availableStorefront struct {
	CountryCode string `plist:"country-code"`
	ID          int64  `plist:"id"`
}

type storefrontBagResult struct {
	StorefrontID            any    `plist:"storeFrontId"`
	CountryCode             string `plist:"currentStorefrontCountryCodeISO2A"`
	AvailableStorefrontsURL string `plist:"available-storefronts"`
	URLBag                  struct {
		AvailableStorefrontsURL string `plist:"available-storefronts"`
	} `plist:"urlBag"`
}

type storefrontMapping struct{ id, country string }

func (t *appstore) countryCodeFromStoreFront(storeFront string) (string, error) {
	if !validStorefrontIdentifier(storefrontIdentifier(storeFront)) {
		return "", storefrontMappingError(storeFront)
	}

	mapping, err := t.discoverStorefront(storeFront, "")
	if err != nil {
		return "", err
	}

	return mapping.country, nil
}

func (t *appstore) storefrontID(countryCode string) string {
	t.storefrontsMu.Lock()
	defer t.storefrontsMu.Unlock()

	return findStorefrontMapping(t.storefrontCountryCodes, "", countryCode).id
}

func (t *appstore) discoverStorefrontID(countryCode string) (string, error) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if !validStorefrontCountry(countryCode) {
		return "", fmt.Errorf("invalid storefront country code (%s)", countryCode)
	}

	mapping, err := t.discoverStorefront("", countryCode)
	if err != nil {
		return "", err
	}

	return mapping.id, nil
}

// A scoped bag identifies its actual storefront, including countries omitted by
// available-storefronts. Validate both fields: Apple defaults unknown IDs to US
// while echoing the requested ID in the response headers.
func (t *appstore) discoverStorefront(storeFront, country string) (storefrontMapping, error) {
	t.storefrontsMu.Lock()
	defer t.storefrontsMu.Unlock()

	identifier := storefrontIdentifier(storeFront)
	if mapping := findStorefrontMapping(t.storefrontCountryCodes, identifier, country); mapping.id != "" {
		return mapping, nil
	}

	if t.storefrontBagClient == nil {
		return storefrontMapping{}, errors.New("storefront bag client is not configured")
	}

	request := t.bagRequest("")
	if identifier != "" {
		request.Headers["X-Apple-Store-Front"] = strings.TrimSpace(storeFront)
	} else {
		request.URL += "&cc=" + url.QueryEscape(country)
	}

	response, err := t.storefrontBagClient.Send(request)
	if err != nil {
		return storefrontMapping{}, fmt.Errorf("storefront bag request failed: %w", err)
	}

	if response.StatusCode != gohttp.StatusOK {
		return storefrontMapping{}, NewErrorWithMetadata(errors.New("storefront bag request failed"), response)
	}

	bag := response.Data
	id := fmt.Sprint(bag.StorefrontID)
	code := strings.ToUpper(strings.TrimSpace(bag.CountryCode))

	if validStorefrontIdentifier(id) && validStorefrontCountry(code) &&
		(identifier == "" || id == identifier) && (country == "" || code == country) {
		if t.storefrontCountryCodes == nil {
			t.storefrontCountryCodes = make(map[string]string)
		}

		t.storefrontCountryCodes[id] = code

		return storefrontMapping{id: id, country: code}, nil
	}

	// Older bag formats can omit the identity metadata. Their advertised list
	// remains a dynamic fallback; never guess an endpoint or a country mapping.
	endpoint := bag.URLBag.AvailableStorefrontsURL
	if endpoint == "" {
		endpoint = bag.AvailableStorefrontsURL
	}

	if err := t.discoverAvailableStorefronts(endpoint); err != nil {
		return storefrontMapping{}, err
	}

	if mapping := findStorefrontMapping(t.storefrontCountryCodes, identifier, country); mapping.id != "" {
		return mapping, nil
	}

	if identifier != "" {
		return storefrontMapping{}, storefrontMappingError(storeFront)
	}

	return storefrontMapping{}, fmt.Errorf("store front mapping for country code (%s) was not found", country)
}

// Called with storefrontsMu held. Partial mappings from scoped bags do not mean
// the full list has been loaded, and loading it must preserve those mappings.
func (t *appstore) discoverAvailableStorefronts(endpoint string) error {
	if t.storefrontListLoaded {
		return nil
	}

	if t.storefrontClient == nil {
		return errors.New("storefront client is not configured")
	}

	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") ||
		(!strings.EqualFold(u.Hostname(), "apple.com") && !strings.HasSuffix(strings.ToLower(u.Hostname()), ".apple.com")) {
		return errors.New("invalid available-storefronts endpoint in bag")
	}

	var lastErr error

	for attempt := 1; attempt <= storefrontDiscoveryAttempts; attempt++ {
		if attempt > 1 {
			storefrontRetrySleep(time.Duration(attempt-1) * storefrontRetryDelay)
		}

		countryCodes, retryable, err := t.fetchAvailableStorefronts(endpoint)
		if err == nil {
			if t.storefrontCountryCodes == nil {
				t.storefrontCountryCodes = make(map[string]string)
			}

			for id, code := range countryCodes {
				if t.storefrontCountryCodes[id] == "" {
					t.storefrontCountryCodes[id] = code
				}
			}

			t.storefrontListLoaded = true

			return nil
		}

		lastErr = err

		if !retryable {
			break
		}
	}

	return lastErr
}

// fetchAvailableStorefronts 取一次店面表；retryable 表示这次失败值得再试
// （网络层错误 / 429 / 5xx）。4xx 与解码问题都是确定性的，重试只会浪费时间。
func (t *appstore) fetchAvailableStorefronts(endpoint string) (map[string]string, bool, error) {
	res, err := t.storefrontClient.Send(http.Request{
		URL: endpoint, Method: http.MethodGET, ResponseFormat: http.ResponseFormatRaw,
		Headers: map[string]string{"Accept": "application/xml", "User-Agent": storeAuthenticationUserAgent},
	})
	if err != nil {
		return nil, true, fmt.Errorf("available storefronts request failed: %w", err)
	}

	if res.StatusCode != gohttp.StatusOK {
		// 状态码必须落在文本里：这层错误最终只会以字符串形式到达界面与日志，
		// 没有它就只能看到「失败了」而不知道是 403 还是 503。
		retryable := res.StatusCode == gohttp.StatusTooManyRequests || res.StatusCode >= gohttp.StatusInternalServerError

		return nil, retryable, NewErrorWithMetadata(
			fmt.Errorf("available storefronts request failed (HTTP %d)", res.StatusCode), res)
	}

	var result availableStorefrontsResult
	if _, err = plist.Unmarshal(res.Data, &result); err != nil {
		return nil, false, fmt.Errorf("failed to decode available storefronts: %w", err)
	}

	countryCodes := make(map[string]string, len(result.Storefronts))

	for _, storefront := range result.Storefronts {
		code := strings.ToUpper(strings.TrimSpace(storefront.CountryCode))
		if storefront.ID > 0 && validStorefrontCountry(code) {
			countryCodes[strconv.FormatInt(storefront.ID, 10)] = code
		}
	}

	if len(countryCodes) == 0 {
		return nil, false, errors.New("available storefronts response contains no storefronts")
	}

	return countryCodes, false, nil
}

func findStorefrontMapping(countryCodes map[string]string, identifier, country string) storefrontMapping {
	if identifier != "" {
		if code := countryCodes[identifier]; code != "" {
			return storefrontMapping{id: identifier, country: code}
		}

		return storefrontMapping{}
	}

	for id, code := range countryCodes {
		if code == country {
			return storefrontMapping{id: id, country: code}
		}
	}

	return storefrontMapping{}
}

func validStorefrontIdentifier(value string) bool {
	identifier, err := strconv.ParseInt(value, 10, 64)

	return err == nil && identifier > 0 && strconv.FormatInt(identifier, 10) == value
}

func validStorefrontCountry(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}

func countryCodeFromStoreFront(storeFront string, countryCodes map[string]string) (string, error) {
	identifier := storefrontIdentifier(storeFront)

	if countryCode, ok := countryCodes[identifier]; ok {
		return countryCode, nil
	}

	return "", storefrontMappingError(storeFront)
}

func storefrontIdentifier(storeFront string) string {
	identifier, _, _ := strings.Cut(strings.TrimSpace(storeFront), ",")
	identifier, _, _ = strings.Cut(identifier, "-")

	return identifier
}

func storefrontMappingError(storeFront string) error {
	return fmt.Errorf("country code mapping for store front (%s) was not found", storeFront)
}
