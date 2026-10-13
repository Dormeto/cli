package aws

// The read-only constraint (the plan's Global constraints): the adapters call
// only Describe*, List* and Get* operations, and sts:AssumeRole. The test walks
// this package's source: every operation is named by its input type
// (<service>.<Operation>Input), and every call of a method an SDK client has
// must be one of those. A later task's adapter extends the list.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// readOnly are the operations the adapters call, by service.
var readOnly = []string{
	"autoscaling.DescribeAutoScalingGroups",
	"cloudwatch.GetMetricData",
	"ec2.DescribeInstances",
	"ec2.DescribeRegions",
	"ec2.DescribeSpotPriceHistory",
	"ecs.DescribeServices",
	"ecs.DescribeTaskDefinition",
	"ecs.ListClusters",
	"ecs.ListServices",
	"eks.DescribeCluster",
	"eks.ListClusters",
	"elasticloadbalancingv2.DescribeListeners",
	"elasticloadbalancingv2.DescribeLoadBalancers",
	"elasticloadbalancingv2.DescribeRules",
	"elasticloadbalancingv2.DescribeTags",
	"elasticloadbalancingv2.DescribeTargetGroups",
	"elasticloadbalancingv2.DescribeTargetHealth",
	"organizations.DescribeOrganization",
	"organizations.ListAccountsForParent",
	"organizations.ListOrganizationalUnitsForParent",
	"organizations.ListRoots",
	"organizations.ListTagsForResource",
	"pricing.GetProducts",
	"rds.DescribeDBClusters",
	"rds.DescribeDBInstances",
	"sts.AssumeRole",
	"sts.GetCallerIdentity",
}

func allowed(op string) bool {
	return strings.HasPrefix(op, "Describe") || strings.HasPrefix(op, "List") || strings.HasPrefix(op, "Get") || op == "AssumeRole"
}

// sdkOperations are the methods of the SDK clients: every operation of every
// service the adapters may import.
func sdkOperations() map[string]bool {
	ops := map[string]bool{}
	for _, c := range []any{&autoscaling.Client{}, &cloudwatch.Client{}, &ec2.Client{}, &ecs.Client{}, &eks.Client{}, &elasticloadbalancingv2.Client{}, &organizations.Client{}, &pricing.Client{}, &rds.Client{}, &sts.Client{}} {
		t := reflect.TypeOf(c)
		for i := range t.NumMethod() {
			ops[t.Method(i).Name] = true
		}
	}
	delete(ops, "Options")
	return ops
}

type found struct {
	operations []string        // <service>.<Operation>, from the input types named
	called     map[string]bool // the SDK client methods called
}

// operationsOf walks the package's non-test sources.
func operationsOf(t *testing.T, dir string) found {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	ops := sdkOperations()
	f := found{called: map[string]bool{}}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		services := map[string]string{} // local name -> service
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			rest, ok := strings.CutPrefix(p, "github.com/aws/aws-sdk-go-v2/service/")
			if !ok || strings.Contains(rest, "/") {
				continue
			}
			name := rest
			if imp.Name != nil {
				name = imp.Name.Name
			}
			services[name] = rest
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if x, ok := n.X.(*ast.Ident); ok {
					if svc, ok := services[x.Name]; ok {
						if op, ok := strings.CutSuffix(n.Sel.Name, "Input"); ok && !seen[svc+"."+op] {
							seen[svc+"."+op] = true
							f.operations = append(f.operations, svc+"."+op)
						}
					}
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && ops[sel.Sel.Name] {
					f.called[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	slices.Sort(f.operations)
	return f
}

func TestTheAdaptersCallOnlyReadOnlyOperations(t *testing.T) {
	f := operationsOf(t, ".")
	for _, op := range f.operations {
		if _, name, _ := strings.Cut(op, "."); !allowed(name) {
			t.Errorf("%s is not a read-only operation", op)
		}
	}
	if !slices.Equal(f.operations, readOnly) {
		t.Errorf("the operations the adapters call:\ngot  %v\nwant %v", f.operations, readOnly)
	}
	names := map[string]bool{}
	for _, op := range f.operations {
		_, name, _ := strings.Cut(op, ".")
		names[name] = true
	}
	for call := range f.called {
		if !allowed(call) {
			t.Errorf("%s is called: not a read-only operation", call)
		}
		if !names[call] {
			t.Errorf("%s is called without its input type: the operation is not listed", call)
		}
	}
}

func TestTheReadOnlyWalkFindsAWriteAndACallWithoutItsInput(t *testing.T) {
	dir := t.TempDir()
	src := `package x

import (
	"context"

	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

func f(c *elb.Client) {
	c.DescribeRules(context.Background(), &elb.DescribeRulesInput{})
	c.DeleteRule(context.Background(), nil)
	c.ModifyRule(context.Background(), &elb.ModifyRuleInput{})
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	f := operationsOf(t, dir)
	if !slices.Equal(f.operations, []string{"elasticloadbalancingv2.DescribeRules", "elasticloadbalancingv2.ModifyRule"}) {
		t.Errorf("operations %v", f.operations)
	}
	if !f.called["DeleteRule"] || !f.called["ModifyRule"] || !f.called["DescribeRules"] {
		t.Errorf("called %v", f.called)
	}
}
