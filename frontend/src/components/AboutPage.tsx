import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { openExternal } from "@/lib/utils";
import { GetOSInfo } from "@/lib/rpc";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Bug, Lightbulb, ExternalLink, } from "lucide-react";
import { DragDropMedia } from "./DragDropTextarea";
interface AboutPageProps {
    version: string;
}
export function AboutPage({ version }: AboutPageProps) {
    const [os, setOs] = useState("Unknown");
    // Deliberately local-only: this used to call ipapi.co to resolve a
    // city/region/country from the visitor's IP, which then got baked into
    // the public GitHub issue body created by handleSubmit below. The
    // browser's own timezone is enough of a hint for triage without
    // silently leaking geolocation to a third party. A lazy initializer
    // needs no effect since it's a synchronous, side-effect-free read.
    const [location] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone);
    const [activeTab, setActiveTab] = useState<"bug_report" | "feature_request">("bug_report");
    const [bugType, setBugType] = useState("Track");
    const [problem, setProblem] = useState("");
    const [spotifyUrl, setSpotifyUrl] = useState("");
    const [bugContext, setBugContext] = useState("");
    const [featureDesc, setFeatureDesc] = useState("");
    const [useCase, setUseCase] = useState("");
    const [featureContext, setFeatureContext] = useState("");
    useEffect(() => {
        const fetchOS = async () => {
            try {
                const info = await GetOSInfo();
                setOs(info);
            }
            catch {
                const userAgent = window.navigator.userAgent;
                if (userAgent.indexOf("Win") !== -1)
                    setOs("Windows");
                else if (userAgent.indexOf("Mac") !== -1)
                    setOs("macOS");
                else if (userAgent.indexOf("Linux") !== -1)
                    setOs("Linux");
            }
        };
        fetchOS();
    }, []);
    const handleSubmit = () => {
        const title = activeTab === "bug_report"
            ? `[Bug Report] ${problem.substring(0, 50)}${problem.length > 50 ? "..." : ""}`
            : `[Feature Request] ${featureDesc.substring(0, 50)}${featureDesc.length > 50 ? "..." : ""}`;
        let bodyContent: string;
        if (activeTab === "bug_report") {
            const contextContent = bugContext.trim()
                ? bugContext.trim()
                : "Type here or send screenshot/recording";
            bodyContent = `### [Bug Report]

#### Problem
${problem || "Type here"}

#### Type
${bugType}

#### Spotify URL
${spotifyUrl || "Type here"}

#### Additional Context
${contextContent}

#### Environment
- SpotiFLAC Version: ${version}
- OS: ${os}
- Location: ${location}`;
        }
        else {
            const contextContent = featureContext.trim()
                ? featureContext.trim()
                : "Type here or send screenshot/recording";
            bodyContent = `### [Feature Request]

#### Description
${featureDesc || "Type here"}

#### Use Case
${useCase || "Type here"}

#### Additional Context
${contextContent}`;
        }
        const params = new URLSearchParams({
            title: title,
            body: bodyContent,
        });
        const url = `https://github.com/sos-pc/SpotiFLAC-SH/issues/new?${params.toString()}`;
        openExternal(url);
    };
    return (<div className="flex flex-col space-y-4">
      <div className="flex items-center justify-between shrink-0">
        <h2 className="text-2xl font-bold tracking-tight">About</h2>
      </div>

      <div className="flex gap-2 border-b shrink-0">
        <Button variant={activeTab === "bug_report" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("bug_report")} className="rounded-b-none">
          <Bug className="h-4 w-4"/>
          Bug Report
        </Button>
        <Button variant={activeTab === "feature_request" ? "default" : "ghost"} size="sm" onClick={() => setActiveTab("feature_request")} className="rounded-b-none">
          <Lightbulb className="h-4 w-4"/>
          Feature Request
        </Button>
      </div>

      <div className="flex-1 min-h-0">
        {activeTab === "bug_report" && (<div className="flex flex-col">
            <div className="space-y-4 pt-4 flex flex-col">
              <div className="mt-4 pr-2">
                <div className="grid md:grid-cols-3 gap-6">
                  <div className="space-y-2 flex flex-col">
                    <Label>Problem</Label>
                    <Textarea className="h-56 resize-none" placeholder="Describe the problem..." value={problem} onChange={(e) => setProblem(e.target.value)}/>
                  </div>
                  <div className="space-y-2 flex flex-col">
                    <Label>Additional Context</Label>
                    <DragDropMedia className="min-h-[14rem]" value={bugContext} onChange={setBugContext}/>
                  </div>
                  <div className="space-y-4 flex flex-col">
                    <div className="space-y-2">
                      <Label>Type</Label>
                      <ToggleGroup type="single" value={bugType} onValueChange={(val) => {
                if (val)
                    setBugType(val);
            }} className="justify-start w-full cursor-pointer">
                        <ToggleGroupItem value="Track" className="flex-1 cursor-pointer" aria-label="Toggle track">
                          Track
                        </ToggleGroupItem>
                        <ToggleGroupItem value="Album" className="flex-1 cursor-pointer" aria-label="Toggle album">
                          Album
                        </ToggleGroupItem>
                        <ToggleGroupItem value="Playlist" className="flex-1 cursor-pointer" aria-label="Toggle playlist">
                          Playlist
                        </ToggleGroupItem>
                        <ToggleGroupItem value="Artist" className="flex-1 cursor-pointer" aria-label="Toggle artist">
                          Artist
                        </ToggleGroupItem>
                      </ToggleGroup>
                    </div>
                    <div className="space-y-2">
                      <Label>Spotify URL</Label>
                      <Input placeholder="https://open.spotify.com/..." value={spotifyUrl} onChange={(e) => setSpotifyUrl(e.target.value)}/>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div className="flex justify-center pt-4 shrink-0">
              <Button className="w-[200px] cursor-pointer gap-2" onClick={handleSubmit}>
                <ExternalLink className="h-4 w-4"/> Create Issue on GitHub
              </Button>
            </div>
          </div>)}

        {activeTab === "feature_request" && (<div className="flex flex-col">
            <div className="space-y-4 pt-4 flex flex-col">
              <div className="mt-4 pr-2">
                <div className="grid md:grid-cols-3 gap-6">
                  <div className="space-y-2 flex flex-col">
                    <Label>Description</Label>
                    <Textarea className="h-56 resize-none" placeholder="Describe your feature request..." value={featureDesc} onChange={(e) => setFeatureDesc(e.target.value)}/>
                  </div>
                  <div className="space-y-2 flex-col">
                    <Label>Use Case</Label>
                    <Textarea className="h-56 resize-none" placeholder="How would this feature be useful?" value={useCase} onChange={(e) => setUseCase(e.target.value)}/>
                  </div>
                  <div className="space-y-2 flex-col">
                    <Label>Additional Context</Label>
                    <DragDropMedia className="min-h-[14rem]" value={featureContext} onChange={setFeatureContext}/>
                  </div>
                </div>
              </div>
            </div>
            <div className="flex justify-center pt-4 shrink-0">
              <Button className="w-[200px] cursor-pointer gap-2" onClick={handleSubmit}>
                <ExternalLink className="h-4 w-4"/> Create Issue on GitHub
              </Button>
            </div>
          </div>)}
      </div>
    </div>);
}
