package previewhost

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/redact"
)

type environment struct {
	app      map[string]string
	services map[string]map[string]string
}

func randomSecret() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

type environmentResolver struct {
	content v1.PreviewBlueprintContentV1
	edge    string
	random  func() (string, error)
	secrets map[string]string
}

func resolveEnvironment(
	content v1.PreviewBlueprintContentV1,
	operation v1.PreviewDeployOperation,
	random func() (string, error),
) (environment, error) {
	resolver := &environmentResolver{content: content, edge: operation.EdgeHost, random: random, secrets: map[string]string{}}
	resolved := environment{services: make(map[string]map[string]string, len(content.Services))}
	for _, service := range content.Services {
		values, err := resolver.literal(service.Env)
		if err != nil {
			return environment{}, err
		}
		resolved.services[service.Name] = values
	}
	app, err := resolver.literal(content.App.Env)
	if err != nil {
		return environment{}, err
	}
	resolved.app = app
	for _, service := range content.Services {
		if err := resolver.urls(service.Env, resolved.services[service.Name], resolved); err != nil {
			return environment{}, err
		}
	}
	if err := resolver.urls(content.App.Env, resolved.app, resolved); err != nil {
		return environment{}, err
	}
	return resolved, nil
}

func (resolver *environmentResolver) literal(env map[string]v1.PreviewBlueprintEnvEntryV1) (map[string]string, error) {
	values := make(map[string]string, len(env))
	for key, entry := range env {
		switch entry.Kind {
		case v1.PreviewBlueprintEnvFake:
			values[key] = entry.Value
		case v1.PreviewBlueprintEnvGenerated:
			if entry.Generator != "random_secret" {
				continue
			}
			secret, ok := resolver.secrets[key]
			if !ok {
				var err error
				if secret, err = resolver.random(); err != nil {
					return nil, err
				}
				redact.RememberSecret(secret)
				resolver.secrets[key] = secret
			}
			values[key] = secret
		}
	}
	return values, nil
}

func (resolver *environmentResolver) urls(env map[string]v1.PreviewBlueprintEnvEntryV1, values map[string]string, resolved environment) error {
	for key, entry := range env {
		if entry.Kind != v1.PreviewBlueprintEnvGenerated || entry.Generator == "random_secret" {
			continue
		}
		value, err := resolver.generatedURL(entry.Generator, resolved)
		if err != nil {
			return fmt.Errorf("environment %s: %w", key, err)
		}
		values[key] = value
	}
	return nil
}

func (resolver *environmentResolver) serviceWithRole(role string) (v1.PreviewBlueprintServiceV1, bool) {
	for _, service := range resolver.content.Services {
		if service.Role == role {
			return service, true
		}
	}
	return v1.PreviewBlueprintServiceV1{}, false
}

func (resolver *environmentResolver) generatedURL(generator string, resolved environment) (string, error) {
	switch generator {
	case "preview_url":
		if resolver.edge != "" {
			return "https://" + resolver.edge, nil
		}
		return "http://127.0.0.1:" + strconv.Itoa(resolver.content.App.Port), nil
	case "postgres_url":
		service, ok := resolver.serviceWithRole("database")
		if !ok {
			return "", fmt.Errorf("postgres_url needs a database service")
		}
		env := resolved.services[service.Name]
		user := env["POSTGRES_USER"]
		if user == "" {
			user = "postgres"
		}
		database := env["POSTGRES_DB"]
		if database == "" {
			database = user
		}
		userinfo := url.User(user)
		if password := env["POSTGRES_PASSWORD"]; password != "" {
			userinfo = url.UserPassword(user, password)
		}
		return (&url.URL{Scheme: "postgres", User: userinfo, Host: service.Name + ":5432", Path: "/" + database}).String(), nil
	case "redis_url":
		service, ok := resolver.serviceWithRole("cache")
		if !ok {
			return "", fmt.Errorf("redis_url needs a cache service")
		}
		return "redis://" + service.Name + ":6379", nil
	case "smtp_url":
		service, ok := resolver.serviceWithRole("mail_sink")
		if !ok {
			return "", fmt.Errorf("smtp_url needs a mail_sink service")
		}
		return "smtp://" + service.Name + ":1025", nil
	}
	return "", fmt.Errorf("generator %q is not supported", generator)
}
