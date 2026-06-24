package k8s

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

var RouteGVR = schema.GroupVersionResource{
	Group:    "route.openshift.io",
	Version:  "v1",
	Resource: "routes",
}

// EnsureRoute creates or updates an OpenShift Route targeting the given service.
// The Route uses edge TLS termination with HTTP-to-HTTPS redirect and is
// owned by the given Deployment for cascade deletion.
func EnsureRoute(ctx context.Context, client dynamic.Interface, name, namespace, serviceName string, servicePort int, deployment *appsv1.Deployment) error {
	expected := buildRouteObject(name, namespace, serviceName, servicePort, deployment)

	existing, err := client.Resource(RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err = client.Resource(RouteGVR).Namespace(namespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create Route: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to get Route: %w", err)
	}

	// Update if spec differs
	expected.SetResourceVersion(existing.GetResourceVersion())
	_, err = client.Resource(RouteGVR).Namespace(namespace).Update(ctx, expected, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update Route: %w", err)
	}
	return nil
}

func buildRouteObject(name, namespace, serviceName string, servicePort int, deployment *appsv1.Deployment) *unstructured.Unstructured {
	route := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "route.openshift.io/v1",
			"kind":       "Route",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"name":       deployment.Name,
						"uid":        string(deployment.UID),
						"controller": true,
					},
				},
			},
			"spec": map[string]interface{}{
				"to": map[string]interface{}{
					"kind":   "Service",
					"name":   serviceName,
					"weight": int64(100),
				},
				"port": map[string]interface{}{
					"targetPort": int64(servicePort),
				},
				"tls": map[string]interface{}{
					"termination":                   "edge",
					"insecureEdgeTerminationPolicy": "Redirect",
				},
			},
		},
	}
	return route
}

// WaitForRouteAdmitted polls until the Route is admitted by the OpenShift router
// and returns the assigned hostname.
func WaitForRouteAdmitted(ctx context.Context, client dynamic.Interface, name, namespace string, timeout time.Duration) (string, error) {
	var host string
	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		route, err := client.Resource(RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		status, ok := route.Object["status"].(map[string]interface{})
		if !ok {
			return false, nil
		}

		ingress, ok := status["ingress"].([]interface{})
		if !ok || len(ingress) == 0 {
			return false, nil
		}

		firstIngress, ok := ingress[0].(map[string]interface{})
		if !ok {
			return false, nil
		}

		conditions, ok := firstIngress["conditions"].([]interface{})
		if !ok {
			return false, nil
		}

		for _, c := range conditions {
			cond, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			condType, _ := cond["type"].(string)
			condStatus, _ := cond["status"].(string)
			if condType == "Admitted" && condStatus == "True" {
				host, _ = firstIngress["host"].(string)
				return host != "", nil
			}
		}

		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("Route %s/%s was not admitted: %w", namespace, name, err)
	}
	return host, nil
}

// GetRouteHost returns the hostname from an existing Route.
// It reads from status.ingress[0].host first, falling back to spec.host.
func GetRouteHost(ctx context.Context, client dynamic.Interface, name, namespace string) (string, error) {
	route, err := client.Resource(RouteGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	// Try status first
	if status, ok := route.Object["status"].(map[string]interface{}); ok {
		if ingress, ok := status["ingress"].([]interface{}); ok && len(ingress) > 0 {
			if firstIngress, ok := ingress[0].(map[string]interface{}); ok {
				if host, ok := firstIngress["host"].(string); ok && host != "" {
					return host, nil
				}
			}
		}
	}

	// Fallback to spec.host
	if spec, ok := route.Object["spec"].(map[string]interface{}); ok {
		if host, ok := spec["host"].(string); ok && host != "" {
			return host, nil
		}
	}

	return "", fmt.Errorf("Route %s/%s has no host", namespace, name)
}
