package keda

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	httpv1alpha1 "github.com/kedacore/http-add-on/operator/apis/http/v1alpha1"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"knative.dev/func/pkg/deployer"
	fn "knative.dev/func/pkg/functions"
	"knative.dev/func/pkg/k8s"
)

const (
	KedaDeployerName = "keda"
)

type DeployerOpt func(*Deployer)

type Deployer struct {
	k8s.Deployer

	verbose   bool
	decorator deployer.DeployDecorator
}

func NewDeployer(opts ...DeployerOpt) *Deployer {
	d := &Deployer{
		Deployer: *k8s.NewDeployer(
			// init with the kedaDeployerDecorator to have the correct deployer labels&annotations
			k8s.WithDeployerDecorator(&kedaDeployerDecorator{}),
		),
	}

	for _, opt := range opts {
		opt(d)
	}
	return d
}

func WithDeployerVerbose(verbose bool) DeployerOpt {
	return func(d *Deployer) {
		d.verbose = verbose
		k8s.WithDeployerVerbose(verbose)(&d.Deployer)
	}
}

func WithDeployerDecorator(decorator deployer.DeployDecorator) DeployerOpt {
	// use the custom keda decorator, which wraps the given decorator,
	// but with the keda specific annotations
	kedaDecorator := &kedaDeployerDecorator{
		wrapper: decorator,
	}

	return func(d *Deployer) {
		d.decorator = kedaDecorator
		k8s.WithDeployerDecorator(kedaDecorator)(&d.Deployer)
	}
}

var _ deployer.DeployDecorator = &kedaDeployerDecorator{}

type kedaDeployerDecorator struct {
	wrapper deployer.DeployDecorator
}

func (k *kedaDeployerDecorator) UpdateAnnotations(function fn.Function, annotations map[string]string) map[string]string {
	if k.wrapper != nil {
		annotations = k.wrapper.UpdateAnnotations(function, annotations)
	}

	// set correct deployer name
	annotations[deployer.DeployerNameAnnotation] = KedaDeployerName

	return annotations
}

func (k *kedaDeployerDecorator) UpdateLabels(function fn.Function, labels map[string]string) map[string]string {
	if k.wrapper != nil {
		labels = k.wrapper.UpdateLabels(function, labels)
	}

	return labels
}

func (d *Deployer) Deploy(ctx context.Context, f fn.Function) (fn.DeploymentResult, error) {
	// execute raw deployment deployer
	deployResult, err := d.Deployer.Deploy(ctx, f)
	if err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to deploy function via raw deployer: %w", err)
	}

	// create additional required keda resources
	namespace := deployResult.Namespace

	k8sClientset, err := k8s.NewKubernetesClientset()
	if err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to create K8sClientset: %v", err)
	}

	deployment, err := k8sClientset.AppsV1().Deployments(namespace).Get(ctx, f.Name, metav1.GetOptions{})
	if err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to get deployment %s/%s: %v", namespace, f.Name, err)
	}

	appService, err := k8sClientset.CoreV1().Services(namespace).Get(ctx, f.Name, metav1.GetOptions{})
	if err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to get service %s/%s: %v", namespace, f.Name, err)
	}

	if err := d.ensureInterceptorBridgeService(ctx, k8sClientset, f, namespace, deployment); err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to ensure proxy service exists: %w", err)
	}

	hosts := []string{
		fmt.Sprintf("%s.%s.svc", d.interceptorBridgeServiceName(f), namespace),
		d.interceptorBridgeServiceName(f),
	}

	// On OpenShift, create a Route to expose the function externally.
	// The Route targets a route-proxy service (no selector) backed by
	// Endpoints pointing to the KEDA interceptor proxy's ClusterIP, so
	// external traffic goes through the interceptor and is counted for
	// autoscaling.
	if k8s.IsOpenShift() {
		interceptorClusterIP, interceptorPort, err := d.getInterceptorProxyAddress(ctx, k8sClientset)
		if err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to get interceptor proxy address: %w", err)
		}

		routeProxyName := d.routeProxyServiceName(f)
		if err := d.ensureRouteProxyService(ctx, k8sClientset, f, namespace, deployment, interceptorPort); err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to ensure route-proxy service: %w", err)
		}

		if err := d.ensureRouteProxyEndpointSlice(ctx, k8sClientset, f, namespace, deployment, interceptorClusterIP, interceptorPort); err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to ensure route-proxy endpointslice: %w", err)
		}

		dynamicClient, err := k8s.NewDynamicClient()
		if err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to create dynamic client: %w", err)
		}

		if err := k8s.EnsureRoute(ctx, dynamicClient, f.Name, namespace, routeProxyName, int(interceptorPort), deployment); err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to ensure OpenShift Route: %w", err)
		}

		routeHost, err := k8s.WaitForRouteAdmitted(ctx, dynamicClient, f.Name, namespace, k8s.DefaultWaitingTimeout)
		if err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("OpenShift Route not admitted: %w", err)
		}

		// Add the Route hostname to the HTTPScaledObject so the interceptor
		// matches external traffic by Host header.
		hosts = append(hosts, routeHost)

		if err := d.ensureHTTPScaledObject(ctx, f, namespace, deployment, appService, hosts); err != nil {
			return fn.DeploymentResult{}, fmt.Errorf("failed to ensure http scaled object exists: %w", err)
		}

		fmt.Fprintf(os.Stderr, "🌐 Function exposed via OpenShift Route: https://%s\n", routeHost)

		return fn.DeploymentResult{
			Status:    deployResult.Status,
			URL:       fmt.Sprintf("https://%s", routeHost),
			Namespace: deployResult.Namespace,
		}, nil
	}

	if err := d.ensureHTTPScaledObject(ctx, f, namespace, deployment, appService, hosts); err != nil {
		return fn.DeploymentResult{}, fmt.Errorf("failed to ensure http scaled object exists: %w", err)
	}

	return fn.DeploymentResult{
		Status:    deployResult.Status,
		URL:       fmt.Sprintf("http://%s:8080", hosts[0]),
		Namespace: deployResult.Namespace,
	}, nil
}

func (d *Deployer) httpScaledObject(f fn.Function, namespace string, deployment *v1.Deployment, service *corev1.Service, hosts []string) (*httpv1alpha1.HTTPScaledObject, error) {
	if len(service.Spec.Ports) == 0 {
		return nil, fmt.Errorf("service %s has no ports defined", service.Name)
	}

	labels, err := deployer.GenerateCommonLabels(f, d.decorator)
	if err != nil {
		return nil, fmt.Errorf("failed to generate common labels: %w", err)
	}

	annotations := deployer.GenerateCommonAnnotations(f, d.decorator, false /*we don't care about dapr for the HttpScaledObject*/, KedaDeployerName)

	minScale := int32(1)
	maxScale := int32(10)
	if scaleOptions := f.Deploy.Options.Scale; scaleOptions != nil {
		if scaleOptions.Min != nil {
			minScale = int32(*scaleOptions.Min)
		}
		if scaleOptions.Max != nil {
			maxScale = int32(*scaleOptions.Max)
		}
	}

	return &httpv1alpha1.HTTPScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name:        f.Name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					UID:        deployment.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Spec: httpv1alpha1.HTTPScaledObjectSpec{
			Hosts: hosts,
			ScaleTargetRef: httpv1alpha1.ScaleTargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployment.Name,
				Service:    service.Name,
				Port:       service.Spec.Ports[0].Port,
			},
			Replicas: &httpv1alpha1.ReplicaStruct{
				Min: &minScale,
				Max: &maxScale,
			},
			CooldownPeriod: ptr.To(int32(300)),
			ScalingMetric: &httpv1alpha1.ScalingMetricSpec{
				Rate: &httpv1alpha1.RateMetricSpec{
					TargetValue: 100,
					Window: metav1.Duration{
						Duration: time.Minute,
					},
					Granularity: metav1.Duration{
						Duration: time.Second,
					},
				},
			},
		},
	}, nil
}

func (d *Deployer) interceptorBridgeServiceName(f fn.Function) string {
	return fmt.Sprintf("%s-interceptor-bridge", f.Name)
}

func (d *Deployer) interceptorBridgeService(f fn.Function, namespace string, deployment *v1.Deployment) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      d.interceptorBridgeServiceName(f),
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					UID:        deployment.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: "keda-add-ons-http-interceptor-proxy.keda.svc.cluster.local",
		},
	}
}

// ensureInterceptorBridgeService makes sure to create the service which serves as the entrypoint to the function
// this service will server as an external-name service and forward the request to the keda interceptor-proxy by
// preserving the host name. This service name is also used in the HTTPScaledObject as host name to allow the
// interceptor to match the request with the correct target/scaledObject.
func (d *Deployer) ensureInterceptorBridgeService(ctx context.Context, clientset *kubernetes.Clientset, f fn.Function, namespace string, deployment *v1.Deployment) error {
	expected := d.interceptorBridgeService(f, namespace, deployment)
	existing, err := clientset.CoreV1().Services(expected.Namespace).Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if _, err := clientset.CoreV1().Services(expected.Namespace).Create(ctx, expected, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("failed to create service to interceptor proxy: %w", err)
			}

			return nil
		}

		return fmt.Errorf("failed to get service to interceptor proxy: %w", err)
	}

	// check if we need to update
	if !equality.Semantic.DeepEqual(existing.Spec, expected.Spec) {
		// Preserve resource version for update
		expected.ResourceVersion = existing.ResourceVersion

		if _, err = clientset.CoreV1().Services(namespace).Update(ctx, expected, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update service to interceptor proxy: %w", err)
		}

		return nil
	}

	return nil
}

func (d *Deployer) ensureHTTPScaledObject(ctx context.Context, f fn.Function, namespace string, deployment *v1.Deployment, service *corev1.Service, hosts []string) error {
	expected, err := d.httpScaledObject(f, namespace, deployment, service, hosts)
	if err != nil {
		return fmt.Errorf("failed to generate http scaled object: %w", err)
	}

	httpScaledObjectClientset, err := NewHTTPScaledObjectClientset()
	if err != nil {
		return fmt.Errorf("failed to create HTTPScaledObject clientset: %v", err)
	}

	existing, err := httpScaledObjectClientset.HttpV1alpha1().HTTPScaledObjects(expected.Namespace).Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if _, err := httpScaledObjectClientset.HttpV1alpha1().HTTPScaledObjects(expected.Namespace).Create(ctx, expected, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("failed to create HTTPScaledObject: %w", err)
			}

			if err := WaitForHTTPScaledObjectAvailable(ctx, httpScaledObjectClientset, namespace, expected.Name, k8s.DefaultWaitingTimeout); err != nil {
				return fmt.Errorf("HTTPScaledObject did not become ready: %w", err)
			}

			return nil
		}

		return fmt.Errorf("failed to get HTTPScaledObject: %w", err)
	}

	// check if we need to update
	if !equality.Semantic.DeepEqual(existing.Spec, expected.Spec) {
		// Preserve resource version for update
		expected.ResourceVersion = existing.ResourceVersion

		if _, err = httpScaledObjectClientset.HttpV1alpha1().HTTPScaledObjects(expected.Namespace).Update(ctx, expected, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update HTTPScaledObject: %w", err)
		}

		if err := WaitForHTTPScaledObjectAvailable(ctx, httpScaledObjectClientset, namespace, expected.Name, k8s.DefaultWaitingTimeout); err != nil {
			return fmt.Errorf("HTTPScaledObject did not become ready: %w", err)
		}

		return nil
	}

	return nil
}

func UsesKedaDeployer(annotations map[string]string) bool {
	deployer, ok := annotations[deployer.DeployerNameAnnotation]

	return ok && deployer == KedaDeployerName
}

const (
	kedaInterceptorProxyServiceName = "keda-add-ons-http-interceptor-proxy"
	kedaNamespace                   = "keda"
)

func (d *Deployer) routeProxyServiceName(f fn.Function) string {
	return fmt.Sprintf("%s-route-proxy", f.Name)
}

// getInterceptorProxyAddress looks up the KEDA HTTP interceptor proxy Service
// in the keda namespace and returns its ClusterIP and port.
func (d *Deployer) getInterceptorProxyAddress(ctx context.Context, clientset *kubernetes.Clientset) (string, int32, error) {
	svc, err := clientset.CoreV1().Services(kedaNamespace).Get(ctx, kedaInterceptorProxyServiceName, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("failed to get interceptor proxy service %s/%s: %w", kedaNamespace, kedaInterceptorProxyServiceName, err)
	}

	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", 0, fmt.Errorf("interceptor proxy service %s/%s has no ClusterIP", kedaNamespace, kedaInterceptorProxyServiceName)
	}

	port := int32(8080)
	if len(svc.Spec.Ports) > 0 {
		port = svc.Spec.Ports[0].Port
	}

	return svc.Spec.ClusterIP, port, nil
}

// ensureRouteProxyService creates or updates a no-selector ClusterIP Service
// in the function namespace that serves as the Route's target.
func (d *Deployer) ensureRouteProxyService(ctx context.Context, clientset *kubernetes.Clientset, f fn.Function, namespace string, deployment *v1.Deployment, port int32) error {
	name := d.routeProxyServiceName(f)

	expected := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					UID:        deployment.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			// No selector — endpoints are managed manually
		},
	}

	existing, err := clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err = clientset.CoreV1().Services(namespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create route-proxy service: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to get route-proxy service: %w", err)
	}

	if !equality.Semantic.DeepEqual(existing.Spec, expected.Spec) {
		expected.ResourceVersion = existing.ResourceVersion
		_, err = clientset.CoreV1().Services(namespace).Update(ctx, expected, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update route-proxy service: %w", err)
		}
	}

	return nil
}

// ensureRouteProxyEndpointSlice creates or updates an EndpointSlice for the
// route-proxy Service, pointing to the KEDA interceptor proxy's ClusterIP.
func (d *Deployer) ensureRouteProxyEndpointSlice(ctx context.Context, clientset *kubernetes.Clientset, f fn.Function, namespace string, deployment *v1.Deployment, interceptorClusterIP string, port int32) error {
	name := d.routeProxyServiceName(f)

	addressType := discoveryv1.AddressTypeIPv4
	if strings.Contains(interceptorClusterIP, ":") {
		addressType = discoveryv1.AddressTypeIPv6
	}

	portName := "http"
	protocol := corev1.ProtocolTCP
	expected := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       deployment.Name,
					UID:        deployment.UID,
					Controller: ptr.To(true),
				},
			},
		},
		AddressType: addressType,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{interceptorClusterIP},
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{
				Name:     &portName,
				Port:     &port,
				Protocol: &protocol,
			},
		},
	}

	existing, err := clientset.DiscoveryV1().EndpointSlices(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, err = clientset.DiscoveryV1().EndpointSlices(namespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create route-proxy endpointslice: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to get route-proxy endpointslice: %w", err)
	}

	if !equality.Semantic.DeepEqual(existing.Endpoints, expected.Endpoints) ||
		!equality.Semantic.DeepEqual(existing.Ports, expected.Ports) {
		expected.ResourceVersion = existing.ResourceVersion
		_, err = clientset.DiscoveryV1().EndpointSlices(namespace).Update(ctx, expected, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update route-proxy endpointslice: %w", err)
		}
	}

	return nil
}
