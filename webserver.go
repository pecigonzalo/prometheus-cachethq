package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

/*
cf https://prometheus.io/docs/alerting/configuration/#webhook_config
{
	"version": "4",
	"groupKey": <string>,    // key identifying the group of alerts (e.g. to deduplicate)
	"status": "<resolved|firing>",
	"receiver": <string>,
	"groupLabels": <object>,
	"commonLabels": <object>,
	"commonAnnotations": <object>,
	"externalURL": <string>,  // backlink to the Alertmanager.
	"alerts": [
	  {
		"labels": <object>,
		"annotations": <object>,
		"startsAt": "<rfc3339>",
		"endsAt": "<rfc3339>"
	  },
	  ...
	]
  }
*/
type PrometheusAlertDetail struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartAt     string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
}

type PrometheusAlert struct {
	Version           string                  `json:"version" binding:"required"`
	GroupKey          string                  `json:"groupKey"`
	Status            string                  `json:"status" binding:"required"`
	Receiver          string                  `json:"receiver"`
	GroupLabels       map[string]string       `json:"groupLabels"`
	CommonLabels      map[string]string       `json:"commonLabels"`
	CommonAnnotations map[string]string       `json:"commonAnnotations"`
	ExternalURL       string                  `json:"externalURL"`
	Alerts            []PrometheusAlertDetail `json:"alerts"`
}

// SubmitAlert receive an alert from Prometheus, and try to forward it to CachetHQ
func SubmitAlert(c *gin.Context, config *PrometheusCachetConfig) {
	// check the Bearer
	if config.PrometheusToken != "" {
		bearer := c.GetHeader("Authorization")
		if bearer != fmt.Sprintf("Bearer %s", config.PrometheusToken) {
			if config.LogLevel == LOG_DEBUG {
				log.Println("wrong Authorization header:", bearer)
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "wrong Authorization header"})
			return
		}
	}

	// read the payload
	var alerts PrometheusAlert
	if err := c.ShouldBindJSON(&alerts); err == nil {
		// talk to CachetHQ
		status := 1 // "resolved"
		componentStatus := 1
		if alerts.Status == "firing" {
			status = 4
			componentStatus = 4
		}

		list, err := config.Cachet.ListComponents()
		if err != nil {
			if config.LogLevel == LOG_DEBUG {
				log.Println(err)
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// prometheus can send 2 times the same alerts info in one call
		alreadyFired := make(map[int]int)
		for _, alert := range alerts.Alerts {
			// fire something
			if componentID, ok := list[alert.Labels[config.LabelName]]; ok {
				if alreadyFired[componentID] == 0 {
					alreadyFired[componentID] = 1

					if config.SquashIncident {
						// firing
						if status != 1 {
							incidents, err := config.Cachet.SearchIncidents(componentID)
							if err != nil {
								c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
								return
							}
							// if no open incident currently, let's create a new one
							if len(incidents) == 0 || incidents[0].Status == 4 {
								if err := config.Cachet.CreateIncident(alert.Labels[config.LabelName], componentID, status, componentStatus); err != nil {
									if config.LogLevel == LOG_DEBUG {
										log.Println(err)
									}
									c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
									return
								}
							}
						} else { // resolved
							// if we want to "squash" event for a given incident
							if incidents, err := config.Cachet.SearchIncidents(componentID); err == nil {
								if len(incidents) > 0 {
									if err == nil {
										config.Cachet.UpdateIncident(alert.Labels[config.LabelName], componentID, incidents[0].Id, status, fmt.Sprintf("Prometheus flagged service %s as up", alert.Labels[config.LabelName]))

										incidentID := incidents[0].Id
										componentName := alert.Labels[config.LabelName]

										if incident, err := config.Cachet.ReadIncident(incidentID); err == nil {
											layout := "2006-01-02 15:04:05"
											createdAt, err1 := time.Parse(layout, incident.CreatedAt)
											updatedAt, err2 := time.Parse(layout, incident.UpdatedAt)

											if err1 == nil && err2 == nil {
												config.Cachet.UpdateIncident(componentName, componentID, incidentID, status, fmt.Sprintf("Prometheus flagged service %s as up (service was down for %d minutes)", componentName, int(updatedAt.Sub(createdAt).Minutes())))
											}
										}
									} else {
										c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
										return
									}
								} else {
									c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("No incident found for component %d\n", componentID)})
									return
								}
							} else {
								c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
								return
							}
						}
					} else { // we dont 'squash' so let's create a new incident
						if err := config.Cachet.CreateIncident(alert.Labels[config.LabelName], componentID, status, componentStatus); err != nil {
							if config.LogLevel == LOG_DEBUG {
								log.Println(err)
							}
							c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
							return
						}
					}
				}
			}
		}

	} else {
		if config.LogLevel == LOG_DEBUG {
			log.Println(err)
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "OK"})
}

func PrepareGinRouter(config *PrometheusCachetConfig) *gin.Engine {
	router := gin.New()
	router.Use(gin.LoggerWithWriter(gin.DefaultWriter, "/health"))
	router.Use(gin.Recovery())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "OK"})
	})

	router.POST("/alert", func(c *gin.Context) {
		SubmitAlert(c, config)
	})

	return router
}
