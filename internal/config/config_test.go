package config

import "testing"

func TestLoadHTTPPortFallsBackToPORT(t *testing.T) {
	t.Setenv("BACKEND_URL", "http://data.test")
	t.Setenv("PIPELINE_API_TOKEN", "test-token")

	cases := []struct {
		name, httpPort, port, want string
	}{
		{"default", "", "", "8082"},
		{"Railway PORT", "", "9000", "9000"},
		{"HTTP_PORT wins", "8082", "9000", "8082"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_PORT", tc.httpPort)
			t.Setenv("PORT", tc.port)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPPort != tc.want {
				t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, tc.want)
			}
		})
	}
}
