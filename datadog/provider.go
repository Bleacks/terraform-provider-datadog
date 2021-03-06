package datadog

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"runtime"
	"strings"
	"time"

	datadogV1 "github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	datadogV2 "github.com/DataDog/datadog-api-client-go/api/v2/datadog"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/terraform-plugin-sdk/helper/logging"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/meta"
	"github.com/hashicorp/terraform-plugin-sdk/terraform"
	"github.com/terraform-providers/terraform-provider-datadog/version"
	datadogCommunity "github.com/zorkian/go-datadog-api"
)

var (
	datadogProvider       *schema.Provider
	baseIpRangesSubdomain = "ip-ranges"
)

func Provider() terraform.ResourceProvider {
	datadogProvider = &schema.Provider{
		Schema: map[string]*schema.Schema{
			"api_key": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.MultiEnvDefaultFunc([]string{"DATADOG_API_KEY", "DD_API_KEY"}, nil),
				Description: "(Required unless validate is false) Datadog API key. This can also be set via the DD_API_KEY environment variable.",
			},
			"app_key": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.MultiEnvDefaultFunc([]string{"DATADOG_APP_KEY", "DD_APP_KEY"}, nil),
				Description: "(Required unless validate is false) Datadog APP key. This can also be set via the DD_APP_KEY environment variable.",
			},
			"api_url": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.MultiEnvDefaultFunc([]string{"DATADOG_HOST", "DD_HOST"}, nil),
				Description: "The API Url. This can be also be set via the DD_HOST environment variable. Note that this URL must not end with the /api/ path. For example, https://api.datadoghq.com/ is a correct value, while https://api.datadoghq.com/api/ is not. And if you're working with \"EU\" version of Datadog, use https://api.datadoghq.eu/.",
			},
			"validate": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Enables validation of the provided API and APP keys during provider initialization. Default is true. When false, api_key and app_keywon't be checked.",
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"datadog_dashboard":                            resourceDatadogDashboard(),
			"datadog_dashboard_list":                       resourceDatadogDashboardList(),
			"datadog_downtime":                             resourceDatadogDowntime(),
			"datadog_integration_aws":                      resourceDatadogIntegrationAws(),
			"datadog_integration_aws_lambda_arn":           resourceDatadogIntegrationAwsLambdaArn(),
			"datadog_integration_aws_log_collection":       resourceDatadogIntegrationAwsLogCollection(),
			"datadog_integration_azure":                    resourceDatadogIntegrationAzure(),
			"datadog_integration_gcp":                      resourceDatadogIntegrationGcp(),
			"datadog_integration_pagerduty":                resourceDatadogIntegrationPagerduty(),
			"datadog_integration_pagerduty_service_object": resourceDatadogIntegrationPagerdutySO(),
			"datadog_logs_archive":                         resourceDatadogLogsArchive(),
			"datadog_logs_archive_order":                   resourceDatadogLogsArchiveOrder(),
			"datadog_logs_custom_pipeline":                 resourceDatadogLogsCustomPipeline(),
			"datadog_logs_index":                           resourceDatadogLogsIndex(),
			"datadog_logs_index_order":                     resourceDatadogLogsIndexOrder(),
			"datadog_logs_integration_pipeline":            resourceDatadogLogsIntegrationPipeline(),
			"datadog_logs_pipeline_order":                  resourceDatadogLogsPipelineOrder(),
			"datadog_metric_metadata":                      resourceDatadogMetricMetadata(),
			"datadog_monitor":                              resourceDatadogMonitor(),
			"datadog_role":                                 resourceDatadogRole(),
			"datadog_screenboard":                          resourceDatadogScreenboard(),
			"datadog_security_monitoring_default_rule":     resourceDatadogSecurityMonitoringDefaultRule(),
			"datadog_security_monitoring_rule":             resourceDatadogSecurityMonitoringRule(),
			"datadog_service_level_objective":              resourceDatadogServiceLevelObjective(),
			"datadog_synthetics_test":                      resourceDatadogSyntheticsTest(),
			"datadog_synthetics_global_variable":           resourceDatadogSyntheticsGlobalVariable(),
			"datadog_synthetics_private_location":          resourceDatadogSyntheticsPrivateLocation(),
			"datadog_timeboard":                            resourceDatadogTimeboard(),
			"datadog_user":                                 resourceDatadogUser(),
		},

		DataSourcesMap: map[string]*schema.Resource{
			"datadog_dashboard":                 dataSourceDatadogDashboard(),
			"datadog_dashboard_list":            dataSourceDatadogDashboardList(),
			"datadog_ip_ranges":                 dataSourceDatadogIpRanges(),
			"datadog_monitor":                   dataSourceDatadogMonitor(),
			"datadog_permissions":               dataSourceDatadogPermissions(),
			"datadog_role":                      dataSourceDatadogRole(),
			"datadog_security_monitoring_rules": dataSourceDatadogSecurityMonitoringRules(),
			"datadog_synthetics_locations":      dataSourceDatadogSyntheticsLocations(),
		},

		ConfigureFunc: providerConfigure,
	}

	return datadogProvider
}

//ProviderConfiguration contains the initialized API clients to communicate with the Datadog API
type ProviderConfiguration struct {
	CommunityClient *datadogCommunity.Client
	DatadogClientV1 *datadogV1.APIClient
	DatadogClientV2 *datadogV2.APIClient
	AuthV1          context.Context
	AuthV2          context.Context

	now func() time.Time
}

func (p *ProviderConfiguration) Now() time.Time {
	return p.now()
}

func providerConfigure(d *schema.ResourceData) (interface{}, error) {
	apiKey := d.Get("api_key").(string)
	appKey := d.Get("app_key").(string)
	validate := d.Get("validate").(bool)

	if validate && (apiKey == "" || appKey == "") {
		return nil, errors.New("api_key and app_key must be set unless validate = false")
	}

	// Initialize the community client
	communityClient := datadogCommunity.NewClient(apiKey, appKey)

	if apiURL := d.Get("api_url").(string); apiURL != "" {
		communityClient.SetBaseUrl(apiURL)
	}

	c := cleanhttp.DefaultClient()
	c.Transport = logging.NewTransport("Datadog", c.Transport)
	communityClient.ExtraHeader["User-Agent"] = getUserAgent(fmt.Sprintf(
		"datadog-api-client-go/%s (go %s; os %s; arch %s)",
		"go-datadog-api",
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	))
	communityClient.HttpClient = c

	if validate {
		log.Println("[INFO] Datadog client successfully initialized, now validating...")
		ok, err := communityClient.Validate()
		if err != nil {
			log.Printf("[ERROR] Datadog Client validation error: %v", err)
			return nil, err
		} else if !ok {
			err := errors.New(`Invalid or missing credentials provided to the Datadog Provider. Please confirm your API and APP keys are valid and are for the correct region, see https://www.terraform.io/docs/providers/datadog/ for more information on providing credentials for the Datadog Provider`)
			log.Printf("[ERROR] Datadog Client validation error: %v", err)
			return nil, err
		}
	} else {
		log.Println("[INFO] Skipping key validation (validate = false)")
	}
	log.Printf("[INFO] Datadog Client successfully validated.")

	// Initialize the official Datadog V1 API client
	authV1 := context.WithValue(
		context.Background(),
		datadogV1.ContextAPIKeys,
		map[string]datadogV1.APIKey{
			"apiKeyAuth": {
				Key: apiKey,
			},
			"appKeyAuth": {
				Key: appKey,
			},
		},
	)
	configV1 := datadogV1.NewConfiguration()
	// Enable unstable operations
	configV1.SetUnstableOperationEnabled("GetLogsIndex", true)
	configV1.SetUnstableOperationEnabled("ListLogIndexes", true)
	configV1.SetUnstableOperationEnabled("UpdateLogsIndex", true)
	configV1.SetUnstableOperationEnabled("UpdateLogsIndexOrder", true)
	configV1.UserAgent = getUserAgent(configV1.UserAgent)
	configV1.Debug = logging.IsDebugOrHigher()
	if apiURL := d.Get("api_url").(string); apiURL != "" {
		parsedApiUrl, parseErr := url.Parse(apiURL)
		if parseErr != nil {
			return nil, fmt.Errorf(`invalid API Url : %v`, parseErr)
		}
		if parsedApiUrl.Host == "" || parsedApiUrl.Scheme == "" {
			return nil, fmt.Errorf(`missing protocol or host : %v`, apiURL)
		}
		// If api url is passed, set and use the api name and protocol on ServerIndex{1}
		authV1 = context.WithValue(authV1, datadogV1.ContextServerIndex, 1)
		authV1 = context.WithValue(authV1, datadogV1.ContextServerVariables, map[string]string{
			"name":     parsedApiUrl.Host,
			"protocol": parsedApiUrl.Scheme,
		})

		// Configure URL's per operation
		// IPRangesApiService.GetIPRanges
		ipRangesDNSNameArr := strings.Split(parsedApiUrl.Hostname(), ".")
		// Parse out subdomain if it exists
		if len(ipRangesDNSNameArr) > 2 {
			ipRangesDNSNameArr = ipRangesDNSNameArr[1:]
		}
		ipRangesDNSNameArr = append([]string{baseIpRangesSubdomain}, ipRangesDNSNameArr...)

		authV1 = context.WithValue(authV1, datadogV1.ContextOperationServerIndices, map[string]int{
			"IPRangesApiService.GetIPRanges": 1,
		})
		authV1 = context.WithValue(authV1, datadogV1.ContextOperationServerVariables, map[string]map[string]string{
			"IPRangesApiService.GetIPRanges": {
				"name": strings.Join(ipRangesDNSNameArr, "."),
			},
		})
	}

	datadogClientV1 := datadogV1.NewAPIClient(configV1)

	// Initialize the official Datadog V2 API client
	authV2 := context.WithValue(
		context.Background(),
		datadogV2.ContextAPIKeys,
		map[string]datadogV2.APIKey{
			"apiKeyAuth": {
				Key: d.Get("api_key").(string),
			},
			"appKeyAuth": {
				Key: d.Get("app_key").(string),
			},
		},
	)
	configV2 := datadogV2.NewConfiguration()
	configV2.UserAgent = getUserAgent(configV2.UserAgent)
	configV2.Debug = logging.IsDebugOrHigher()
	if apiURL := d.Get("api_url").(string); apiURL != "" {
		parsedApiUrl, parseErr := url.Parse(apiURL)
		if parseErr != nil {
			return nil, fmt.Errorf(`invalid API Url : %v`, parseErr)
		}
		if parsedApiUrl.Host == "" || parsedApiUrl.Scheme == "" {
			return nil, fmt.Errorf(`missing protocol or host : %v`, apiURL)
		}
		// If api url is passed, set and use the api name and protocol on ServerIndex{1}
		authV2 = context.WithValue(authV2, datadogV2.ContextServerIndex, 1)
		authV2 = context.WithValue(authV2, datadogV2.ContextServerVariables, map[string]string{
			"name":     parsedApiUrl.Host,
			"protocol": parsedApiUrl.Scheme,
		})
	}

	datadogClientV2 := datadogV2.NewAPIClient(configV2)

	return &ProviderConfiguration{
		CommunityClient: communityClient,
		DatadogClientV1: datadogClientV1,
		DatadogClientV2: datadogClientV2,
		AuthV1:          authV1,
		AuthV2:          authV2,

		now: time.Now,
	}, nil
}

func translateClientError(err error, msg string) error {
	if msg == "" {
		msg = "an error occurred"
	}

	if apiErr, ok := err.(datadogV1.GenericOpenAPIError); ok {
		return fmt.Errorf(msg+": %v: %s", err, apiErr.Body())
	}
	if apiErr, ok := err.(datadogV2.GenericOpenAPIError); ok {
		return fmt.Errorf(msg+": %v: %s", err, apiErr.Body())
	}
	if errUrl, ok := err.(*url.Error); ok {
		return fmt.Errorf(msg+" (url.Error): %s", errUrl)
	}

	return fmt.Errorf(msg+": %s", err.Error())
}

func getUserAgent(clientUserAgent string) string {
	return fmt.Sprintf("terraform-provider-datadog/%s (terraform %s; terraform-cli %s) %s",
		version.ProviderVersion,
		meta.SDKVersionString(),
		datadogProvider.TerraformVersion,
		clientUserAgent)
}
