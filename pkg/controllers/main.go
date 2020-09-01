package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog"

	"k8s.io/client-go/informers"
	schedulingv1beta1 "volcano.sh/volcano/pkg/apis/scheduling/v1beta1"
	vcclientset "volcano.sh/volcano/pkg/client/clientset/versioned"
	"volcano.sh/volcano/pkg/kube"
)

func createContainers(img, command, workingDir string, req, limit corev1.ResourceList, hostport int32) []corev1.Container {
	var imageRepo []string
	container := corev1.Container{
		Image:           img,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Resources: corev1.ResourceRequirements{
			Requests: req,
			Limits:   limit,
		},
	}
	if strings.Index(img, ":") < 0 {
		imageRepo = strings.Split(img, "/")
	} else {
		imageRepo = strings.Split(img[:strings.Index(img, ":")], "/")
	}
	container.Name = imageRepo[len(imageRepo)-1]

	if len(command) > 0 {
		container.Command = []string{"/bin/sh"}
		container.Args = []string{"-c", command}
	}

	if hostport > 0 {
		container.Ports = []corev1.ContainerPort{
			{
				ContainerPort: hostport,
				HostPort:      hostport,
			},
		}
	}

	if len(workingDir) > 0 {
		container.WorkingDir = workingDir
	}

	return []corev1.Container{container}
}

var resourceList = corev1.ResourceList{"cpu": resource.MustParse("20m"), "memory": resource.MustParse("20Mi")}

func main() {

	podsNum := pflag.Int32("pod-number", 5000, "pod numbers to be created")
	kubeConfig := pflag.String("kube-config", "/root/.kube/config", "kubeconfig file")

	opts := kube.ClientOptions{
		KubeConfig: *kubeConfig,
		QPS:        500,
		Burst:      1000,
	}

	config, _ := kube.BuildConfig(opts)

	pg := &schedulingv1beta1.PodGroup{
		ObjectMeta: v1.ObjectMeta{},
		Spec: schedulingv1beta1.PodGroupSpec{
			MinMember:    1,
			MinResources: &resourceList,
		},
	}

	pod := &corev1.Pod{
		TypeMeta: v1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: v1.ObjectMeta{
			Annotations: map[string]string{schedulingv1beta1.KubeGroupNameAnnotationKey: "placeholder"},
		},
		Spec: corev1.PodSpec{
			SchedulerName: "volcano",
			Containers:    createContainers("nginx:1.14", "", "", resourceList, resourceList, 0),
		},
	}

	kubeClient := kubeclientset.NewForConfigOrDie(config)
	vcClient := vcclientset.NewForConfigOrDie(config)

	podMap := make(map[string]struct{}, *podsNum)

	tstart := time.Now()
	var podIndex int32
	for {
		t0 := time.Now()
		// create 500 pods/s
		for i := 0; i < 500; i++ {
			pg.Name = fmt.Sprintf("test-%v", podIndex)
			_, err := vcClient.SchedulingV1beta1().PodGroups("default").Create(context.TODO(), pg, v1.CreateOptions{})
			if err != nil {
				klog.Infof("Error creating podgroup %s %v", pg.Name, err)
				return
			}
			pod.Name = fmt.Sprintf("test-%v", podIndex)
			pod.Annotations[schedulingv1beta1.KubeGroupNameAnnotationKey] = pg.Name
			_, err = kubeClient.CoreV1().Pods("default").Create(context.TODO(), pod, v1.CreateOptions{})
			if err != nil {
				klog.Infof("Error creating pod %s %v", pod.Name, err)
				return
			}

			podMap[pod.Name] = struct{}{}

			podIndex++
			if podIndex == *podsNum {
				goto WAIT
			}
		}

		klog.Infof("it takes %dms to create 500 pods & podgroups", time.Since(t0).Milliseconds())
		time.Sleep(1 * time.Second)
	}
WAIT:
	klog.Infof("it takes %ds to create %d pods & podgroups", *podsNum, time.Since(tstart).Seconds())

	ch := make(chan struct{})
	factory := informers.NewSharedInformerFactory(kubeClient, 0)
	podInformer := factory.Core().V1().Pods()
	go podInformer.Informer().Run(ch)

	wait.PollImmediateUntil(500*time.Millisecond, func() (done bool, err error) {
		for key := range podMap {
			pod, _ := podInformer.Lister().Pods("default").Get(key)
			if pod.Status.Phase == "Running" {
				delete(podMap, key)
			}
		}

		if len(podMap) == 0 {
			return true, nil
		}

		return false, nil
	}, ch)

	klog.Infof("it takes %ds to create %d pods & podgroups and wait all running", *podsNum, time.Since(tstart).Seconds())

	for i := 0; i < int(*podsNum); i++ {
		name := fmt.Sprintf("test-%v", i)
		err := vcClient.SchedulingV1beta1().PodGroups("default").Delete(context.TODO(), name, v1.DeleteOptions{})
		if err != nil {
			klog.Infof("Error delete podgroup %s %v", name, err)
		}
		err = kubeClient.CoreV1().Pods("default").Delete(context.TODO(), name, v1.DeleteOptions{})
		if err != nil {
			klog.Infof("Error delete pod %s %v", name, err)
		}
	}
}
