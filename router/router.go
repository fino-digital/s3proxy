package router

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/mirakl/s3proxy/backend"
	"github.com/mirakl/s3proxy/middleware"
	logging "github.com/op/go-logging"
)

var (
	log = logging.MustGetLogger("s3proxy")
)

// Create a gin router
func NewGinEngine(ginMode string, version string, urlExpiration time.Duration, serverAPIKey string, s3Backend backend.Backend) *gin.Engine {

	gin.SetMode(ginMode)

	engine := gin.New()
	// we do not want any path hinting, this is not supposed to be used interactively, but programmatically.
	engine.RedirectTrailingSlash = false

	// Use middleware for logger, authorization
	engine.Use(middleware.NewLogger(log, "/"), middleware.NewRecovery(log), middleware.NewAuthorization(serverAPIKey, "/"))

	// health check
	engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"response": "ok", "version": version})
	})

	presignedURLApiV1 := engine.Group("/api/v1/presigned/url")

	// create presigned url for a file upload
	presignedURLApiV1.POST("/:bucket/*key", func(c *gin.Context) {

		var (
			bucket     = c.Param("bucket")
			key        = c.Param("key")
			expiration = c.Query("expiration")
		)

		urlExpiration, err := parseExpiration(expiration, urlExpiration)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse Duration " + expiration})
			return
		}

		url, err := s3Backend.CreatePresignedURLForUpload(backend.BucketObject{BucketName: bucket, Key: key}, urlExpiration)
		if err != nil {
			log.Errorf("Failed to create presigned PutObject URL for %s %v", key, bucket, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create PutObject URL for " + key})
			return
		}

		c.JSON(http.StatusOK, gin.H{"url": url})
	})

	// create presigned url for a file download
	presignedURLApiV1.GET("/:bucket/*key", func(c *gin.Context) {

		var (
			bucket     = c.Param("bucket")
			key        = c.Param("key")
			expiration = c.Query("expiration")
		)

		urlExpiration, err := parseExpiration(expiration, urlExpiration)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse Duration " + expiration})
			return
		}

		url, err := s3Backend.CreatePresignedURLForDownload(backend.BucketObject{BucketName: bucket, Key: key}, urlExpiration)
		if err != nil {
			log.Errorf("Failed to create presigned GetObject URL for %s %v", key, bucket, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GetObject URL for " + key})
			return
		}

		c.JSON(http.StatusOK, gin.H{"url": url})
	})

	objectAPIV1 := engine.Group("/api/v1/object")

	type DeleteForm struct {
		Key []string `form:"key" binding:"required"`
	}

	objectAPIV1.POST("/delete/:bucket", func(c *gin.Context) {

		var body DeleteForm
		if err := c.Bind(&body); err != nil {
			log.Errorf("Failed to parse body %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse body %s " + err.Error()})
			return
		}

		bucket := c.Param("bucket")

		objectsToDelete := make([]backend.BucketObject, len(body.Key))

		for index, key := range body.Key {
			objectsToDelete[index] = backend.BucketObject{
				BucketName: bucket,
				Key:        key,
			}
		}

		err := s3Backend.BatchDeleteObjects(objectsToDelete)

		if err != nil {
			log.Errorf("Failed to delete %d objects in bucket %s: %v", len(objectsToDelete), bucket, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete objects " + bucket})
			return
		}

		c.JSON(http.StatusOK, gin.H{"response": "ok"})
	})

	objectAPIV1.DELETE("/:bucket/*key", func(c *gin.Context) {

		var (
			bucket = c.Param("bucket")
			key    = c.Param("key")
		)

		err := s3Backend.DeleteObject(backend.BucketObject{BucketName: bucket, Key: key})

		if err != nil {
			log.Errorf("Failed to delete object %s in bucket %s: %v", key, bucket, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete object " + key})
			return
		}

		c.JSON(http.StatusOK, gin.H{"response": "ok"})
	})

	objectAPIV1.POST("/copy/:bucket/*key", func(c *gin.Context) {

		var (
			sourceBucket      = c.Param("bucket")
			sourceKey         = c.Param("key")
			destinationBucket = c.Query("destBucket")
			destinationKey    = c.Query("destKey")
		)

		if destinationBucket == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing destination bucket"})
			return
		}

		if destinationKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing destination key"})
			return
		}

		err := s3Backend.CopyObject(backend.BucketObject{BucketName: sourceBucket, Key: sourceKey},
			backend.BucketObject{BucketName: destinationBucket, Key: destinationKey})

		if err != nil {
			log.Errorf("Failed to copy object %s %s to %s %s: %v", sourceBucket, sourceKey, destinationBucket, destinationKey, err)

			status, msg := http.StatusInternalServerError, fmt.Sprintf("Failed to copy object : sourceBucket=%q, sourceKey=%q", sourceBucket, sourceKey)

			if err, ok := err.(awserr.Error); ok {
				switch err.Code() {
				case s3.ErrCodeNoSuchBucket:
					status, msg = http.StatusNotFound, fmt.Sprintf("No such bucket : %q or %q", sourceBucket, destinationBucket)
				case s3.ErrCodeNoSuchKey:
					status, msg = http.StatusNotFound, fmt.Sprintf("No such key : %q", sourceKey)
				}
			}

			c.JSON(status, gin.H{"error": msg})

			return
		}

		c.JSON(http.StatusOK, gin.H{"response": "ok"})
	})

	return engine
}

func parseExpiration(s string, fallback time.Duration) (time.Duration, error) {
	if s == "" {
		return fallback, nil
	}

	return time.ParseDuration(s)
}
