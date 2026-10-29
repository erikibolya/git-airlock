package install

import (
	"strings"
	"testing"
)

func TestTaskXMLRunsAirlockDirectly(t *testing.T) {
	xml := taskXML(1, `C:\Program Files\Git Airlock\airlock.exe`, `worker --hidden --config "C:\Users\erik\.airlock.toml"`)
	for _, want := range []string{
		"<Interval>PT1M</Interval>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<Command>C:\\Program Files\\Git Airlock\\airlock.exe</Command>",
		"<Arguments>worker --hidden --config &#34;C:\\Users\\erik\\.airlock.toml&#34;</Arguments>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML is missing %q:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "wscript") || strings.Contains(xml, ".vbs") {
		t.Errorf("task XML still references the VBS wrapper:\n%s", xml)
	}
}

func TestTaskXMLClampsMinutes(t *testing.T) {
	// The caller clamps the interval; the template must still render a whole
	// number of minutes rather than a Go duration.
	xml := taskXML(5, `C:\airlock.exe`, "worker --hidden")
	if !strings.Contains(xml, "<Interval>PT5M</Interval>") {
		t.Errorf("expected PT5M interval:\n%s", xml)
	}
}
