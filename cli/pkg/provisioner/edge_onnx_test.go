package provisioner

import "testing"

func TestSelectEdgeONNXProfile(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		mode      string
		osName    string
		arch      string
		hasNVIDIA bool
		hasIntel  bool
		want      string
		wantErr   bool
	}{
		{name: "apple native auto", mode: "native", osName: "darwin", arch: "arm64", want: "coreml"},
		{name: "apple container auto", mode: "container", osName: "darwin", arch: "arm64", want: "cpu"},
		{name: "nvidia auto", mode: "native", osName: "linux", arch: "amd64", hasNVIDIA: true, hasIntel: true, want: "cuda"},
		{name: "intel auto", mode: "container", osName: "linux", arch: "amd64", hasIntel: true, want: "openvino"},
		{name: "arm cpu fallback", mode: "native", osName: "linux", arch: "arm64", want: "cpu"},
		{name: "explicit tensorrt", requested: "tensorrt", mode: "container", osName: "linux", arch: "amd64", hasNVIDIA: true, want: "tensorrt"},
		{name: "nvidia absent", requested: "cuda", mode: "native", osName: "linux", arch: "amd64", wantErr: true},
		{name: "coreml in container", requested: "coreml", mode: "container", osName: "darwin", arch: "arm64", wantErr: true},
		{name: "unknown", requested: "magic", mode: "native", osName: "linux", arch: "amd64", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectEdgeONNXProfile(test.requested, test.mode, test.osName, test.arch, test.hasNVIDIA, test.hasIntel)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("profile = %q, want %q", got, test.want)
			}
		})
	}
}
