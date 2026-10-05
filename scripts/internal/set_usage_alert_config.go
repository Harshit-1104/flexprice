package internal

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/cache"
	"github.com/flexprice/flexprice/internal/config"
	domainSettings "github.com/flexprice/flexprice/internal/domain/settings"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/postgres"
	"github.com/flexprice/flexprice/internal/repository"
	"github.com/flexprice/flexprice/internal/tracing"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/flexprice/flexprice/internal/utils"
)

// SetUsageAlertConfig upserts the Flexprice-owned usage_alert_config setting for one environment.
// Env: TENANT_ID, ENVIRONMENT_ID, SCHEDULE_DELAY_SECONDS, STALE_AFTER_SECONDS (0 = deployment default).
func SetUsageAlertConfig() error {
	tenantID := strings.TrimSpace(os.Getenv("TENANT_ID"))
	environmentID := strings.TrimSpace(os.Getenv("ENVIRONMENT_ID"))
	if tenantID == "" || environmentID == "" {
		return fmt.Errorf("TENANT_ID and ENVIRONMENT_ID are required")
	}

	scheduleDelay, err := intFromEnv("SCHEDULE_DELAY_SECONDS")
	if err != nil {
		return err
	}
	staleAfter, err := intFromEnv("STALE_AFTER_SECONDS")
	if err != nil {
		return err
	}

	usageAlertCfg := types.UsageAlertConfig{ScheduleDelaySeconds: scheduleDelay, StaleAfterSeconds: staleAfter}
	if err := usageAlertCfg.Validate(); err != nil {
		return fmt.Errorf("invalid usage_alert_config: %w", err)
	}
	value, err := utils.ToMap(usageAlertCfg)
	if err != nil {
		return err
	}

	cfg, err := config.NewConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	log, err := logger.NewLogger(cfg)
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	entClient, err := postgres.NewEntClients(cfg, log)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres: %w", err)
	}
	settingsRepo := repository.NewSettingsRepository(repository.RepositoryParams{
		EntClient:     postgres.NewClient(entClient, log, tracing.NewService(cfg, log)),
		Logger:        log,
		InMemoryCache: cache.GetInMemoryCache(),
	})

	ctx := context.WithValue(context.Background(), types.CtxTenantID, tenantID)
	ctx = context.WithValue(ctx, types.CtxEnvironmentID, environmentID)
	ctx = context.WithValue(ctx, types.CtxUserID, types.DefaultUserID)

	existing, err := settingsRepo.GetByKey(ctx, types.SettingKeyUsageAlertConfig)
	switch {
	case err == nil:
		previous := existing.Value
		existing.Value = value
		existing.UpdatedAt = time.Now().UTC()
		existing.UpdatedBy = types.DefaultUserID
		if err := settingsRepo.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update setting: %w", err)
		}
		fmt.Printf("Updated %s (%s): %v -> %v\n", existing.ID, types.SettingKeyUsageAlertConfig, previous, value)
	case ierr.IsNotFound(err):
		setting := &domainSettings.Setting{
			ID:            types.GenerateUUIDWithPrefix(types.UUID_PREFIX_SETTING),
			Key:           types.SettingKeyUsageAlertConfig,
			Value:         value,
			EnvironmentID: environmentID,
			BaseModel:     types.GetDefaultBaseModel(ctx),
		}
		if err := settingsRepo.Create(ctx, setting); err != nil {
			return fmt.Errorf("failed to create setting: %w", err)
		}
		fmt.Printf("Created %s (%s): %v\n", setting.ID, types.SettingKeyUsageAlertConfig, value)
	default:
		return fmt.Errorf("failed to read setting: %w", err)
	}

	fmt.Println("Servers pick this up within the settings cache TTL (~2 minutes).")
	return nil
}

func intFromEnv(name string) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer number of seconds: %w", name, err)
	}
	return v, nil
}
