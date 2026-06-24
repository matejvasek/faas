//go:build integration

package keda_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"

	fn "knative.dev/func/pkg/functions"
	"knative.dev/func/pkg/k8s"
	"knative.dev/func/pkg/keda"
)

var routeGVR = schema.GroupVersionResource{
	Group:    "route.openshift.io",
	Version:  "v1",
	Resource: "routes",
}

var projectGVR = schema.GroupVersionResource{
	Group:    "project.openshift.io",
	Version:  "v1",
	Resource: "projectrequests",
}

// ocpProject creates an OCP project (and thus namespace) via ProjectRequest
// and returns the project name. Registers cleanup to delete the project.
func ocpProject(t *testing.T, ctx context.Context) string {
	t.Helper()

	name := "func-int-route-" + rand.String(5)

	dynamicClient, err := k8s.NewDynamicClient()
	if err != nil {
		t.Fatalf("creating dynamic client: %v", err)
	}

	projectRequest := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "project.openshift.io/v1",
			"kind":       "ProjectRequest",
			"metadata": map[string]interface{}{
				"name": name,
			},
		},
	}

	_, err = dynamicClient.Resource(projectGVR).Create(ctx, projectRequest, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating OCP project %q: %v", name, err)
	}

	t.Cleanup(func() {
		projectGVR := schema.GroupVersionResource{
			Group:    "project.openshift.io",
			Version:  "v1",
			Resource: "projects",
		}
		err := dynamicClient.Resource(projectGVR).Delete(context.Background(), name, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("error deleting OCP project %q: %v", name, err)
		}
	})

	t.Logf("created OCP project: %s", name)
	return name
}

// TestInt_RouteCreated deploys a function with the KEDA deployer and verifies
// that an OpenShift Route is created pointing to the route-proxy service,
// with edge TLS termination, and owned by the Deployment.
func TestInt_RouteCreated(t *testing.T) {
	if !k8s.IsOpenShift() {
		t.Skip("not an OpenShift cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ns := ocpProject(t, ctx)
	funcName := "route-test"

	deployer := keda.NewDeployer(keda.WithDeployerVerbose(true))

	f := fn.Function{
		Name:    funcName,
		Runtime: "go",
		Deploy: fn.DeploySpec{
			Image:     "gcr.io/google-samples/hello-app:2.0",
			Namespace: ns,
		},
	}

	result, err := deployer.Deploy(ctx, f)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	t.Logf("deploy result: %+v", result)

	t.Cleanup(func() {
		remover := keda.NewRemover(false)
		if err := remover.Remove(context.Background(), funcName, ns); err != nil {
			t.Logf("error removing function: %v", err)
		}
	})

	// 1. Verify returned URL is HTTPS (Route-based)
	if !strings.HasPrefix(result.URL, "https://") {
		t.Errorf("expected URL to start with https://, got: %s", result.URL)
	}

	// 2. Verify Route exists on the cluster
	dynamicClient, err := k8s.NewDynamicClient()
	if err != nil {
		t.Fatalf("creating dynamic client: %v", err)
	}

	route, err := dynamicClient.Resource(routeGVR).Namespace(ns).Get(ctx, funcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Route %q not found in namespace %q: %v", funcName, ns, err)
	}

	// 3. Verify Route spec
	spec, ok := route.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("Route has no spec")
	}

	// Check TLS termination
	tlsSpec, ok := spec["tls"].(map[string]interface{})
	if !ok {
		t.Fatal("Route has no tls spec")
	}
	if termination, _ := tlsSpec["termination"].(string); termination != "edge" {
		t.Errorf("expected tls.termination=edge, got %q", termination)
	}
	if policy, _ := tlsSpec["insecureEdgeTerminationPolicy"].(string); policy != "Redirect" {
		t.Errorf("expected insecureEdgeTerminationPolicy=Redirect, got %q", policy)
	}

	// Check target service
	to, ok := spec["to"].(map[string]interface{})
	if !ok {
		t.Fatal("Route has no spec.to")
	}
	targetName, _ := to["name"].(string)
	expectedTarget := fmt.Sprintf("%s-route-proxy", funcName)
	if targetName != expectedTarget {
		t.Errorf("expected Route to target service %q, got %q", expectedTarget, targetName)
	}

	// 4. Verify ownerReference points to the Deployment
	ownerRefs, ok := route.Object["metadata"].(map[string]interface{})["ownerReferences"].([]interface{})
	if !ok || len(ownerRefs) == 0 {
		t.Fatal("Route has no ownerReferences")
	}
	firstOwner, _ := ownerRefs[0].(map[string]interface{})
	if kind, _ := firstOwner["kind"].(string); kind != "Deployment" {
		t.Errorf("expected ownerReference kind=Deployment, got %q", kind)
	}
	if ownerName, _ := firstOwner["name"].(string); ownerName != funcName {
		t.Errorf("expected ownerReference name=%q, got %q", funcName, ownerName)
	}

	// 5. Verify route-proxy service exists with correct endpoints
	k8sClient, err := k8s.NewKubernetesClientset()
	if err != nil {
		t.Fatalf("creating k8s clientset: %v", err)
	}

	proxySvc, err := k8sClient.CoreV1().Services(ns).Get(ctx, expectedTarget, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("route-proxy service %q not found: %v", expectedTarget, err)
	}
	// Verify it's a no-selector service (proxy)
	if len(proxySvc.Spec.Selector) > 0 {
		t.Errorf("expected route-proxy service to have no selector, got %v", proxySvc.Spec.Selector)
	}

	endpoints, err := k8sClient.CoreV1().Endpoints(ns).Get(ctx, expectedTarget, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("endpoints %q not found: %v", expectedTarget, err)
	}
	if len(endpoints.Subsets) == 0 || len(endpoints.Subsets[0].Addresses) == 0 {
		t.Fatal("endpoints have no addresses (should point to interceptor proxy ClusterIP)")
	}

	// 6. Verify HTTPScaledObject hosts include the Route hostname
	httpScaledObjectClientset, err := keda.NewHTTPScaledObjectClientset()
	if err != nil {
		t.Fatalf("creating HTTPScaledObject clientset: %v", err)
	}
	hso, err := httpScaledObjectClientset.HttpV1alpha1().HTTPScaledObjects(ns).Get(ctx, funcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("HTTPScaledObject not found: %v", err)
	}

	// Extract the Route hostname
	routeHost := ""
	status, _ := route.Object["status"].(map[string]interface{})
	if ingress, ok := status["ingress"].([]interface{}); ok && len(ingress) > 0 {
		firstIngress, _ := ingress[0].(map[string]interface{})
		routeHost, _ = firstIngress["host"].(string)
	}
	if routeHost == "" {
		routeHost, _ = spec["host"].(string)
	}
	if routeHost == "" {
		t.Fatal("could not determine Route hostname")
	}
	t.Logf("Route hostname: %s", routeHost)

	found := false
	for _, host := range hso.Spec.Hosts {
		if host == routeHost {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected HTTPScaledObject hosts to contain Route hostname %q, got %v", routeHost, hso.Spec.Hosts)
	}
}

// TestInt_RouteAccessible deploys a function and verifies the Route URL is
// accessible externally via HTTPS.
func TestInt_RouteAccessible(t *testing.T) {
	if !k8s.IsOpenShift() {
		t.Skip("not an OpenShift cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ns := ocpProject(t, ctx)
	funcName := "route-access"

	deployer := keda.NewDeployer(keda.WithDeployerVerbose(true))

	f := fn.Function{
		Name:    funcName,
		Runtime: "go",
		Deploy: fn.DeploySpec{
			Image:     "gcr.io/google-samples/hello-app:2.0",
			Namespace: ns,
		},
	}

	result, err := deployer.Deploy(ctx, f)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	t.Cleanup(func() {
		remover := keda.NewRemover(false)
		if err := remover.Remove(context.Background(), funcName, ns); err != nil {
			t.Logf("error removing function: %v", err)
		}
	})

	if !strings.HasPrefix(result.URL, "https://") {
		t.Fatalf("expected HTTPS URL, got: %s", result.URL)
	}

	// The Route uses the OCP default wildcard cert which may not be trusted
	// by the system CA pool, so we skip TLS verification.
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 30 * time.Second,
	}

	// Poll until the Route is serving (HAProxy might need a moment)
	var lastErr error
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", result.URL, nil)
		if err != nil {
			return false, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			return false, nil // retry
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Logf("Route is accessible: %s (status=%d)", result.URL, resp.StatusCode)
			return true, nil
		}
		lastErr = fmt.Errorf("unexpected status: %d", resp.StatusCode)
		return false, nil // retry
	})
	if err != nil {
		t.Fatalf("Route never became accessible at %s: %v (last error: %v)", result.URL, err, lastErr)
	}
}

// TestInt_RouteCascadeDelete deploys a function, verifies the Route exists,
// then removes the function and verifies the Route is garbage-collected
// via ownerReference cascade.
func TestInt_RouteCascadeDelete(t *testing.T) {
	if !k8s.IsOpenShift() {
		t.Skip("not an OpenShift cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ns := ocpProject(t, ctx)
	funcName := "route-cascade"

	deployer := keda.NewDeployer(keda.WithDeployerVerbose(true))
	remover := keda.NewRemover(false)

	f := fn.Function{
		Name:    funcName,
		Runtime: "go",
		Deploy: fn.DeploySpec{
			Image:     "gcr.io/google-samples/hello-app:2.0",
			Namespace: ns,
		},
	}

	_, err := deployer.Deploy(ctx, f)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	dynamicClient, err := k8s.NewDynamicClient()
	if err != nil {
		t.Fatalf("creating dynamic client: %v", err)
	}

	// Verify Route exists before removal
	_, err = dynamicClient.Resource(routeGVR).Namespace(ns).Get(ctx, funcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Route should exist before removal: %v", err)
	}

	// Remove the function (deletes Deployment, cascade should clean up Route)
	if err := remover.Remove(ctx, funcName, ns); err != nil {
		t.Fatalf("remove failed: %v", err)
	}

	// Wait for Route to be garbage-collected
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := dynamicClient.Resource(routeGVR).Namespace(ns).Get(ctx, funcName, metav1.GetOptions{})
		if err != nil {
			return true, nil // Route is gone
		}
		return false, nil // Still exists, keep polling
	})
	if err != nil {
		t.Fatal("Route was not garbage-collected after Deployment deletion")
	}
	t.Log("Route was successfully garbage-collected via ownerReference cascade")
}

// TestInt_RouteDescriber deploys a function and verifies the Describer returns
// the Route URL.
func TestInt_RouteDescriber(t *testing.T) {
	if !k8s.IsOpenShift() {
		t.Skip("not an OpenShift cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ns := ocpProject(t, ctx)
	funcName := "route-describe"

	deployer := keda.NewDeployer(keda.WithDeployerVerbose(true))
	describer := keda.NewDescriber(true)

	f := fn.Function{
		Name:    funcName,
		Runtime: "go",
		Deploy: fn.DeploySpec{
			Image:     "gcr.io/google-samples/hello-app:2.0",
			Namespace: ns,
		},
	}

	_, err := deployer.Deploy(ctx, f)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	t.Cleanup(func() {
		remover := keda.NewRemover(false)
		if err := remover.Remove(context.Background(), funcName, ns); err != nil {
			t.Logf("error removing function: %v", err)
		}
	})

	instance, err := describer.Describe(ctx, funcName, ns)
	if err != nil {
		t.Fatalf("describe failed: %v", err)
	}

	t.Logf("instance Route: %s", instance.Route)
	t.Logf("instance Routes: %v", instance.Routes)

	if !strings.HasPrefix(instance.Route, "https://") {
		t.Errorf("expected primary Route URL to start with https://, got: %s", instance.Route)
	}

	// Should have both external Route URL and internal URLs
	if len(instance.Routes) < 2 {
		t.Errorf("expected at least 2 routes (external + internal), got %d: %v", len(instance.Routes), instance.Routes)
	}
}

// TestInt_RouteLister deploys a function and verifies the Lister returns
// the Route URL.
func TestInt_RouteLister(t *testing.T) {
	if !k8s.IsOpenShift() {
		t.Skip("not an OpenShift cluster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	ns := ocpProject(t, ctx)
	funcName := "route-list"

	deployer := keda.NewDeployer(keda.WithDeployerVerbose(true))
	lister := keda.NewLister(true)

	f := fn.Function{
		Name:    funcName,
		Runtime: "go",
		Deploy: fn.DeploySpec{
			Image:     "gcr.io/google-samples/hello-app:2.0",
			Namespace: ns,
		},
	}

	_, err := deployer.Deploy(ctx, f)
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	t.Cleanup(func() {
		remover := keda.NewRemover(false)
		if err := remover.Remove(context.Background(), funcName, ns); err != nil {
			t.Logf("error removing function: %v", err)
		}
	})

	items, err := lister.List(ctx, ns)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 function in list, got %d", len(items))
	}

	if !strings.HasPrefix(items[0].URL, "https://") {
		t.Errorf("expected list URL to start with https://, got: %s", items[0].URL)
	}
	t.Logf("list URL: %s", items[0].URL)
}
