package main

import (
	"flag"
	"fmt"
	"github.com/mitchellh/go-homedir"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/golang/glog"

	core_v1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

type host struct {
	hostname string
	ip       string
}

type Controller struct {
	indexer  cache.Indexer
	queue    workqueue.RateLimitingInterface
	informer cache.Controller
}

const (
	deafultHostsFile = "hosts.txt"
)

var (
	hostsFile = flag.String("hostsFilePath", deafultHostsFile, "Hosts file path.")
)

var serverStartTime = time.Now()

var kubeClient = func() kubernetes.Interface {
	var ret kubernetes.Interface
	config, err := rest.InClusterConfig()
	if err != nil {
		var kubeconfigPath string
		if os.Getenv("KUBECONFIG") == "" {
			home, err := homedir.Dir()
			if err != nil {
				panic(err)
			}
			kubeconfigPath = home + "/.kube/config"
		} else {
			kubeconfigPath = os.Getenv("KUBECONFIG")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			panic(err)
		}
	}
	ret, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}
	return ret
}()

func updateHostsFile() error {
	lines, _ := fileLines()
	hosts := getHosts()
	filtered := filterLine(lines, hosts)
	err := writeNewHosts(filtered, hosts)
	return err
}

func getNodeList() ([]core_v1.Node, error) {
	out, err := kubeClient.CoreV1().Nodes().List(meta_v1.ListOptions{})
	return out.Items, err
}

func getHosts() []host {
	var ret []host
	ns, e := getNodeList()
	if e != nil {
		glog.Errorln(e)
	}
	for _, n := range ns {
		var h, i string
		for _, a := range n.Status.Addresses {
			if a.Type == "Hostname" {
				h = a.Address
			}
			if a.Type == "InternalIP" {
				i = a.Address
			}
		}
		ret = append(ret, host{h, i})
	}
	return ret
}

func fileLines() ([]string, error) {
	input, err := ioutil.ReadFile(*hostsFile)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(input), "\n"), nil
}

func filterLine(strs []string, hosts []host) []string {
	var ret []string
CONT:
	for i, s := range strs {
		for _, h := range hosts {
			if strings.Contains(s, h.hostname) {
				continue CONT
			}
		}
		if len(strs) == i+1 && s == "" {
			continue
		}
		ret = append(ret, s)
	}
	return ret
}

func writeNewHosts(strs []string, hosts []host) error {
	file, err := os.Create(*hostsFile)
	if err != nil {
		return err
	}
	for _, s := range strs {
		file.WriteString(s + "\n")
	}
	for _, h := range hosts {
		file.WriteString(h.ip + "\t" + h.hostname + "\n")
	}
	file.WriteString("")
	file.Close()
	return nil
}

func kubeletStartSelect() fields.Selector {
	var selectors []fields.Selector
	selectors = append(selectors, fields.OneTermEqualSelector("involvedObject.kind", "Node"))
	selectors = append(selectors, fields.OneTermEqualSelector("source", "kubelet"))
	selectors = append(selectors, fields.OneTermEqualSelector("reason", "Starting"))
	return fields.AndSelectors(selectors...)
}

func watchStart() {
	eventListWatcher := cache.NewListWatchFromClient(kubeClient.CoreV1().RESTClient(), "events", "", kubeletStartSelect())
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	indexer, informer := cache.NewIndexerInformer(eventListWatcher, &core_v1.Event{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})
	controller := NewController(queue, indexer, informer)
	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)
	select {}
}

func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller) *Controller {
	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
	}
}

func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()
	glog.Info("Starting Pod controller : ", serverStartTime)

	go c.informer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	glog.Info("Stopping Pod controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func (c *Controller) processNextItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	obj, exists, err := c.indexer.GetByKey(key.(string))
	if err != nil {
		c.handleErr(err, key)
	}
	if exists {
		if ev, ok := obj.(*core_v1.Event); ok {
			if ev.ObjectMeta.CreationTimestamp.Sub(serverStartTime).Seconds() > 0 {
				glog.Info("detect kubelet start, update hosts file")
				updateHostsFile()
			}
		}
	}
	return true
}

func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}
	c.queue.Forget(key)
}

func main() {
	flag.Parse()
	err := updateHostsFile()
	if err != nil {
		glog.Errorln(err)
	}
	watchStart()
}
