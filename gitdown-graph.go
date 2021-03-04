// gitdown generates GitHub download graphs
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v33/github"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"
)

const dateForm = "2006-01-02"

var (
	repoFlag      = flag.String("repo", "", "GitHub repo to inquire about")
	tokenPathFlag = flag.String("token-path", "", "GitHub token path")

	ignoreAssetRe = regexp.MustCompile(`\.sha256|VERSION`)
)

const htmlTmpl = `<html>
<head>
    <title>{{ .Repo }} - Release stats</title>
    <link rel="preconnect" href="https://fonts.gstatic.com">
    <link href="https://fonts.googleapis.com/css2?family=Open+Sans:wght@300;400;600;700&display=swap" rel="stylesheet">
    <script type="text/javascript" src="https://www.gstatic.com/charts/loader.js"></script>
    <script type="text/javascript">
        google.charts.load("current", {packages:["corechart"]});
    </script>
    <style>
    body {
       font-family: 'Open Sans', sans-serif;
       background-color: #f7f7fa;
       padding: 1em;
    }

    h1 {
      color: rgba(66,133,244);
      margin-bottom: 0em;
    }

    .subtitle {
      color: rgba(23,90,201);
      font-size: small;
    }

    pre {
        white-space: pre-wrap;
        word-wrap: break-word;
        color: #666;
        font-size: small;
    }

    h2.cli {
       color: #666;
    }

    h2 {
        color: #333;
    }

    .board p {
        font-size: small;
        color: #999;
        text-align: center;
    }


    .board {
        clear: right;
        display: inline-block;
        padding: 0.5em;
        margin: 0.5em;
        background-color: #fff;
    }
    .board:nth-child(4n+3) {
        border: 2px solid rgba(66,133,244,0.25);
        color: rgba(66,133,244);
    }

    .board:nth-child(4n+2) {
        border: 2px solid rgba(219,68,55,0.25);
        color: rgba rgba(219,68,55);
    }

    .board:nth-child(4n+1) {
        border: 2px solid rgba(244,160,0,0.25);
        color: rgba(244,160,0);
    }

    .board:nth-child(4n) {
        border: 2px solid rgba(15,157,88,0.25);
        color: rgba(15,157,88);
    }

    h3 {
        text-align: center;
    }

    </style>
</head>
<body>
    <h1>{{.Repo}}</h1>
    <div class="subtitle">Release Statistics</div>

    <h2 class="cli">Command-line</h2>
    <pre>{{.Command}}</pre>

	<h2>Release Frequency</h2>

	<div id="releaseFreq" style="height: 400px"></div>
	<script type="text/javascript">
		google.charts.setOnLoadCallback(drawReleaseFreq);

		function drawReleaseFreq() {
			var data = new google.visualization.arrayToDataTable([
			['Release', 'Days active', { role: 'annotation' }],
			{{ range .Releases }}{{ if not .Prerelease }}["{{.Name}}", {{.DaysActive}}, "{{printf "%0.f" .DaysActive}}"],{{ end }}
			{{ end }}
			]);

			var options = {
			axisTitlesPosition: 'none',

			bars: 'horizontal', // Required for Material Bar Charts.
			axes: {
				x: {
				y: { side: 'top'} // Top x-axis.
				}
			},
			legend: { position: "none" },
			bar: { groupWidth: "85%" }
			};

		   var chart = new google.visualization.ColumnChart(document.getElementById('releaseFreq'));
		   chart.draw(data, options);
		};
	</script>
	</div>

	<h2>Asset Mix (latest release)</h2>
	<div id="assetMix" style="height: 400px"></div>
	<script type="text/javascript">
	google.charts.setOnLoadCallback(assetMix);
	function assetMix() {
		var data = new google.visualization.arrayToDataTable([
			['Asset', 'Downloads', { role: 'annotation' }],
			{{ range $key, $value := .Latest.Downloads }}["{{$key}}", {{$value}}, "{{$key}} ({{$value}})"],
			{{ end }}
			]);
	  var chart = new google.visualization.PieChart(document.getElementById('assetMix'));
	  var options = {};
	  chart.draw(data, options);
	}
  </script>


	<h2>Downloads Per Day</h2>

	<div id="downloadAvg" style="height: 400px; width: 98%"></div>
	<script type="text/javascript">
		google.charts.setOnLoadCallback(drawDownloadAvg);

		function drawDownloadAvg() {
			var data = new google.visualization.arrayToDataTable([
			['Release', 'Downloads per day', { role: 'annotation' }],
			{{ range .Releases }}{{ if not .Prerelease }}["{{.Name}}", {{.DownloadsPerDay}}, "{{.DownloadsTotal}}"],{{ end }}
			{{ end }}
			]);

			var options = {
			axisTitlesPosition: 'none',

			bars: 'horizontal', // Required for Material Bar Charts.
			axes: {
				x: {
				y: { side: 'top'} // Top x-axis.
				}
			},
			legend: { position: "none" },
			bar: { groupWidth: "85%" }
			};

		   var chart = new google.visualization.ColumnChart(document.getElementById('downloadAvg'));
		   chart.draw(data, options);
		};
	</script>
	</div>

	<div id="downloadAvgTime" style="height: 400px; width: 98%"></div>
	<script type="text/javascript">
		google.charts.setOnLoadCallback(drawDownloadAvgTime);

		function drawDownloadAvgTime() {
			var data = new google.visualization.arrayToDataTable([
			['Date', 'Downloads per day'],
			{{ range .Releases }}{{ if not .Prerelease }}["{{.ActiveUntil | Date}}", {{.DownloadsPerDay}}],{{ end }}
			{{ end }}
			]);

			var options = {
			};

		   var chart = new google.visualization.LineChart(document.getElementById('downloadAvgTime'));
		   chart.draw(data, options);
		};
	</script>
	</div>

</body>
</html>
`

func main() {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
	flag.Set("alsologtostderr", "true")

	flag.Parse()

	if *repoFlag == "" || *tokenPathFlag == "" {
		fmt.Println("usage: gitdown --repo <repository> --token-path <github token path>")
		os.Exit(2)
	}

	ctx := context.Background()
	token, err := ioutil.ReadFile(*tokenPathFlag)
	if err != nil {
		klog.Exitf("token file: %v", err)
	}

	tc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: strings.TrimSpace(string(token))}))
	c := github.NewClient(tc)

	org, repo := parseRepo(*repoFlag)
	rs, err := releases(ctx, c, org, repo)
	if err != nil {
		klog.Exitf("gather failed: %v", err)
	}

	out, err := render(*repoFlag, rs)
	if err != nil {
		klog.Exitf("render failed: %v", err)
	}

	fmt.Print(out)
}

// parseRepo returns the organization and project for a URL or partial path
func parseRepo(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err == nil {
		p := strings.Split(u.Path, "/")
		if u.Hostname() != "" {
			return p[1], p[2]
		}
		return p[0], p[1]
	}
	// Not a URL
	p := strings.Split(rawURL, "/")
	return p[0], p[1]
}

// release represents a processed release
type release struct {
	Name            string
	Draft           bool
	Prerelease      bool
	PublishedAt     time.Time
	ActiveUntil     time.Time
	DaysActive      float64
	DownloadsTotal  int64
	DownloadsPerDay float64
	Downloads       map[string]int
	DownloadRatios  map[string]float64
}

// releases returns a list of pull requests in a project
func releases(ctx context.Context, c *github.Client, org string, project string) ([]*release, error) {
	var result []*release

	opts := &github.ListOptions{PerPage: 100}

	klog.Infof("Downloading releases for %s/%s ...", org, project)

	for page := 1; page != 0; {
		opts.Page = page
		rs, resp, err := c.Repositories.ListReleases(ctx, org, project, opts)
		if err != nil {
			return result, err
		}

		page = resp.NextPage
		until := time.Now()

		for _, r := range rs {
			name := r.GetName()
			if name == "" {
				name = r.GetTagName()
			}

			rel := &release{
				Name:           name,
				Draft:          r.GetDraft(),
				Prerelease:     r.GetPrerelease(),
				PublishedAt:    r.GetPublishedAt().Time,
				ActiveUntil:    until,
				Downloads:      map[string]int{},
				DownloadRatios: map[string]float64{},
			}

			for _, a := range r.Assets {
				if ignoreAssetRe.MatchString(a.GetName()) {
					continue
				}
				rel.Downloads[a.GetName()] = a.GetDownloadCount()
				rel.DownloadsTotal += int64(a.GetDownloadCount())
			}

			if !rel.Draft && !rel.Prerelease {
				until = rel.PublishedAt
			}

			result = append(result, rel)
		}
	}

	for _, r := range result {
		r.DaysActive = r.ActiveUntil.Sub(r.PublishedAt).Hours() / 24
		r.DownloadsPerDay = float64(r.DownloadsTotal) / r.DaysActive

		for k, v := range r.Downloads {
			r.DownloadRatios[k] = float64(v) / float64(r.DownloadsTotal)
		}
	}

	return result, nil
}

func dateStr(t time.Time) string {
	return t.Format(dateForm)
}

func render(repo string, rs []*release) (string, error) {
	funcMap := template.FuncMap{"Date": dateStr}
	tmpl, err := template.New("Releases").Funcs(funcMap).Parse(htmlTmpl)
	if err != nil {
		return "", fmt.Errorf("parse tmpl: %v", err)
	}

	data := struct {
		Title     string
		Repo      string
		Command   string
		BarHeight int
		Releases  []*release
		Latest    *release
	}{
		Repo:      repo,
		Command:   filepath.Base(os.Args[0]) + " " + strings.Join(os.Args[1:], " "),
		BarHeight: 64 + (20 * len(rs)),
		Releases:  rs,
		Latest:    rs[0],
	}

	var tpl bytes.Buffer
	if err = tmpl.Execute(&tpl, data); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}

	out := tpl.String()
	return out, nil
}
