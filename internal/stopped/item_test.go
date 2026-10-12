package stopped

import (
	"encoding/json"
	"testing"
)

func TestItemJSONRoundTripsEachKindAsNodeWritesIt(t *testing.T) {
	for _, in := range []string{
		`{"kind":"ec2","type":"t3.medium","count":2,"os":"Linux"}`,
		`{"kind":"ec2","type":"t3.medium","count":0,"os":"Linux","spot":true}`,
		`{"kind":"fargate","vcpu":0.5,"memoryGb":1,"count":2,"arch":"x86_64","os":"Linux","spot":true}`,
		`{"kind":"rds","class":"db.t4g.micro","engine":"postgres","multiAz":false}`,
		`{"kind":"aurora-serverless","engine":"aurora-mysql","acu":0.5}`,
		`{"kind":"k8s-pods","count":2,"cpu":0.5,"memoryGb":1,"nodeType":"m5.large","os":"Linux","autoscaled":false,"allocatable":{"cpu":1.93,"memoryGb":6.886}}`,
		`{"kind":"k8s-pods","count":2,"cpu":0.5,"memoryGb":1,"nodeType":null,"os":"Linux","spot":true,"autoscaled":true,"allocatable":{"cpu":0,"memoryGb":0}}`,
		`{"kind":"unknown","backend":"ecs:x","reason":"denied"}`,
	} {
		var item Item
		if err := json.Unmarshal([]byte(in), &item); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		out, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		// nodeType null reads as absent: Marshal writes it back as null.
		if string(out) != in {
			t.Errorf("round trip\n in:  %s\n out: %s", in, out)
		}
	}
}
