package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

type RedisConfig struct {
	Addr     string `json:"addr"`
	Password string `json:"password"`
	DB       int    `json:"db"`
}

type ClientConfig struct {
	APIKey         string      `json:"apiKey"`
	TimeoutSeconds int         `json:"timeoutSeconds"`
	RedisConfig    RedisConfig `json:"redisConfig"`
	CacheDuration  int         `json:"cacheDuration"`
}

// No-op logger implementation
// This is used when no logger is provided to the client constructor
// by passing nil as a parameter for the logger
type noopLogger struct{}

func (l *noopLogger) Enabled(ctx context.Context, level slog.Level) bool { return false }
func (l *noopLogger) Handle(ctx context.Context, r slog.Record) error    { return nil }
func (l *noopLogger) WithAttrs(attrs []slog.Attr) slog.Handler           { return l }
func (l *noopLogger) WithGroup(name string) slog.Handler                 { return l }

// VSportsClient_s is the main client struct
// This is the struct that will be used to interact with the API
type VSportsClient_s struct {
	apiKey        string
	baseURL       string
	client        *http.Client
	redisClient   *redis.Client
	cacheDuration time.Duration
	logger        *slog.Logger
}

// VSportsClient is the constructor for the VSportsClient_s struct
func VSportsClient(config ClientConfig, logger *slog.Logger) (*VSportsClient_s, error) {

	// If no logger is provided, use a no-op logger
	if logger == nil {
		logger = slog.New(&noopLogger{}) // Use no-op logger if nil
	}

	// Create a new Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     config.RedisConfig.Addr,
		Password: config.RedisConfig.Password,
		DB:       config.RedisConfig.DB,
	})

	// Ping the Redis server to check if the connection is established
	_, err := rdb.Ping(context.Background()).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &VSportsClient_s{
		apiKey:        config.APIKey,
		baseURL:       "https://extended.vsports.pt/api",
		client:        &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second},
		redisClient:   rdb,
		cacheDuration: time.Duration(config.CacheDuration) * time.Second,
		logger:        logger,
	}, nil
}

// A generic request handler for all API requests
// It can deal with query parameters and caching
func (c *VSportsClient_s) request(endpoint string, params map[string]string, useCache bool) ([]byte, error) {
	ctx := context.Background()

	// Sort and serialize params
	// They need to be sorted to be consistant with any order of the parameters called
	// Serialization is necessary to create a cache key
	var sortedParams []string
	for k, v := range params {
		sortedParams = append(sortedParams, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(sortedParams)
	serializedParams := strings.Join(sortedParams, "&")

	// Use a namespace for the cache key for protection against cache pollution
	cacheKey := fmt.Sprintf("vsports://%s:%s", endpoint, serializedParams)

	// Check if the cache is enabled and if the key exists
	// If so, immediately return the cached response
	if useCache {
		cachedResponse, err := c.redisClient.Get(ctx, cacheKey).Result()
		if err == nil {
			c.logger.Debug(fmt.Sprintf("Using cached response for %s", cacheKey))
			return []byte(cachedResponse), nil
		}
		c.logger.Debug(fmt.Sprintf("Cache miss for %s: %v", cacheKey, err))
	}

	// So we have a cache miss. Make the request to the API
	url := fmt.Sprintf("%s/%s", c.baseURL, endpoint)
	c.logger.Debug(fmt.Sprintf("Making request to URL: %s", url))

	// Create the request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.logger.Error(fmt.Sprintf("Error creating request: %v", err))
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	// Add the parameters to the request if any
	if params != nil {
		q := req.URL.Query()
		for key, value := range params {
			q.Add(key, value)
		}
		req.URL.RawQuery = q.Encode()
	}

	// Add the Authorization header
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))

	// Finally, make the request
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Error(fmt.Sprintf("Error making request: %v", err))
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body as an array of bytes
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(fmt.Sprintf("Error reading response body: %v", err))
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	// If we're using cache, it's time to cache the response
	if useCache {
		err = c.redisClient.Set(ctx, cacheKey, body, c.cacheDuration).Err()
		if err != nil {
			c.logger.Error(fmt.Sprintf("Error setting cache for %s: %v", cacheKey, err))
			return nil, fmt.Errorf("error setting cache for %s: %w", cacheKey, err)
		}
		c.logger.Debug(fmt.Sprintf("Cached response for %s", cacheKey))
	}

	return body, nil
}

// ===== API Methods =====

func (c *VSportsClient_s) GetTournaments(useCache bool) ([]Tournament, error) {
	body, err := c.request("tournaments", nil, useCache)
	if err != nil {
		return nil, err
	}

	var tournaments []Tournament
	err = json.Unmarshal(body, &tournaments)
	return tournaments, err
}

func (c *VSportsClient_s) GetTournamentById(tournamentID int, useCache bool) (*Tournament, error) {
	body, err := c.request(fmt.Sprintf("tournaments/%d", tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var tournament Tournament
	err = json.Unmarshal(body, &tournament)
	return &tournament, err
}

func (c *VSportsClient_s) GetTeamById(teamID int, useCache bool) (*Team, error) {
	body, err := c.request(fmt.Sprintf("teams/%d", teamID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var team Team
	err = json.Unmarshal(body, &team)
	return &team, err
}

func (c *VSportsClient_s) GetTeamsByTournamentId(tournamentID int, useCache bool) ([]Team, error) {
	body, err := c.request(fmt.Sprintf("teams/by/tournament/%d", tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var teams []Team
	err = json.Unmarshal(body, &teams)
	return teams, err
}

func (c *VSportsClient_s) GetEventsByDate(startDate string, endDate string, useCache bool) ([]Event, error) {
	params := map[string]string{
		"start_date": startDate,
		"end_date":   endDate,
	}

	body, err := c.request("events", params, useCache)
	if err != nil {
		return nil, err
	}

	var events []Event
	err = json.Unmarshal(body, &events)
	return events, err
}

func (c *VSportsClient_s) GetEventsDetailedByDate(startDate string, endDate string, useCache bool) ([]Event, error) {
	params := map[string]string{
		"end_date":   endDate,
		"start_date": startDate,
	}
	body, err := c.request("events/detailed", params, useCache)
	if err != nil {
		return nil, err
	}

	var events []Event
	err = json.Unmarshal(body, &events)
	return events, err
}

func (c *VSportsClient_s) GetEventById(eventID int, useCache bool) (*Event, error) {
	body, err := c.request(fmt.Sprintf("events/%d", eventID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var event Event
	err = json.Unmarshal(body, &event)
	return &event, err
}

func (c *VSportsClient_s) GetEventDetailed(eventID int, useCache bool) (*Event, error) {
	body, err := c.request(fmt.Sprintf("events/%d/detailed", eventID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var event Event
	err = json.Unmarshal(body, &event)
	return &event, err
}

func (c *VSportsClient_s) GetEventOccurrences(eventID string, useCache bool) ([]Event, error) {
	body, err := c.request(fmt.Sprintf("events/%s/occurrences", eventID), nil, useCache)
	if err != nil {
		return nil, err
	}

	// This method may return a single event or an array of events
	// Ensure we always return an array
	var response []Event
	err = json.Unmarshal(body, &response)
	if err != nil {
		var singleEvent Event
		err = json.Unmarshal(body, &singleEvent)
		if err != nil {
			return nil, err
		}
		response = append(response, singleEvent)
	}
	return response, nil

}

func (c *VSportsClient_s) GetEventMedia(eventID string, useCache bool) ([]Media_s, error) {
	body, err := c.request(fmt.Sprintf("events/%s/occurrences", eventID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var event Event
	err = json.Unmarshal(body, &event)
	if err != nil {
		return nil, err
	}

	var media []Media_s
	for _, occ := range event.Occurrence {
		media = append(media, occ.Media...)
	}

	return media, nil
}

func (c *VSportsClient_s) GetPersonById(PersonID int, useCache bool) (*Person, error) {
	body, err := c.request(fmt.Sprintf("person/%d", PersonID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var person Person
	err = json.Unmarshal(body, &person)
	return &person, err
}

func (c *VSportsClient_s) GetSquad(teamID int, useCache bool) (*Squad, error) {
	body, err := c.request(fmt.Sprintf("squads/%d", teamID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var squad Squad
	err = json.Unmarshal(body, &squad)
	return &squad, err
}

func (c *VSportsClient_s) GetSquadDetailed(teamID int, useCache bool) (*Squad, error) {
	body, err := c.request(fmt.Sprintf("squads/%d/detailed", teamID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var squad Squad
	err = json.Unmarshal(body, &squad)
	return &squad, err
}

func (c *VSportsClient_s) GetSquadByTournament(teamID, tournamentID int, useCache bool) (*Squad, error) {
	body, err := c.request(fmt.Sprintf("squads/%d/by/tournament/%d", teamID, tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var squad Squad
	err = json.Unmarshal(body, &squad)
	return &squad, err
}

func (c *VSportsClient_s) GetSquadDetailedByTournament(teamID, tournamentID int, useCache bool) (*Squad, error) {
	body, err := c.request(fmt.Sprintf("squads/%d/by/tournament/%d/detailed", teamID, tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var squad Squad
	err = json.Unmarshal(body, &squad)
	return &squad, err
}

func (c *VSportsClient_s) GetStandingsByTournament(tournamentID int, useCache bool) (*Standings, error) {
	body, err := c.request(fmt.Sprintf("standings/by/tournament/%d", tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var standings Standings
	err = json.Unmarshal(body, &standings)
	return &standings, err
}

func (c *VSportsClient_s) GetStandingsByTournamentLive(tournamentID int, useCache bool) (*Standings, error) {
	body, err := c.request(fmt.Sprintf("standings/by/tournament/%d/live", tournamentID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var standings Standings
	err = json.Unmarshal(body, &standings)
	return &standings, err
}

func (c *VSportsClient_s) GetVenue(venueID int, useCache bool) (*Venue, error) {
	body, err := c.request(fmt.Sprintf("venues/%d", venueID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var venue Venue
	err = json.Unmarshal(body, &venue)
	return &venue, err
}

func (c *VSportsClient_s) GetVenuesByTeam(teamID int, useCache bool) ([]Venue, error) {
	body, err := c.request(fmt.Sprintf("venues/by/team/%d", teamID), nil, useCache)
	if err != nil {
		return nil, err
	}

	var venues []Venue
	err = json.Unmarshal(body, &venues)
	return venues, err
}
