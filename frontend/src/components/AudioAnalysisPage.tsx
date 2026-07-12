import { useState, useCallback, useRef } from "react";
import { Button } from "@/components/ui/button";
import { Upload, ArrowLeft, Trash2 } from "lucide-react";
import { AudioAnalysis } from "@/components/AudioAnalysis";
import { SpectrumVisualization } from "@/components/SpectrumVisualization";
import { useAudioAnalysis } from "@/hooks/useAudioAnalysis";
interface AudioAnalysisPageProps {
    onBack?: () => void;
}
export function AudioAnalysisPage({ onBack }: AudioAnalysisPageProps) {
    const { analyzing, result, analyzeFile, clearResult, selectedFilePath, spectrumLoading } = useAudioAnalysis();
    const [isDragging, setIsDragging] = useState(false);
    const fileInputRef = useRef<HTMLInputElement>(null);
    const uploadAndAnalyze = useCallback(async (file: File) => {
        const formData = new FormData();
        formData.append("file", file);
        try {
            const res = await fetch("/api/upload", { method: "POST", body: formData });
            const data = await res.json();
            if (data.path) await analyzeFile(data.path);
        } catch (err) {
            console.error("Upload failed:", err);
        }
    }, [analyzeFile]);
    const handleSelectFile = () => fileInputRef.current?.click();
    const handleFilePicked = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
        const file = e.target.files?.[0];
        e.target.value = "";
        if (file) await uploadAndAnalyze(file);
    }, [uploadAndAnalyze]);
    const handleFileDrop = useCallback(async (e: React.DragEvent) => {
        e.preventDefault();
        setIsDragging(false);
        const items = Array.from(e.dataTransfer.files);
        if (items.length === 0) return;
        await uploadAndAnalyze(items[0]);
    }, [uploadAndAnalyze]);

    const handleAnalyzeAnother = () => {
        clearResult();
    };
    return (<div className="space-y-6">
      
      <input ref={fileInputRef} type="file" accept=".flac" className="hidden" onChange={handleFilePicked}/>

      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4">
          {onBack && (<Button variant="ghost" size="icon" onClick={onBack}>
              <ArrowLeft className="h-5 w-5"/>
            </Button>)}
          <h1 className="text-2xl font-bold">Audio Quality Analyzer</h1>
        </div>
        {result && (<Button onClick={handleAnalyzeAnother} variant="outline" size="sm">
            <Trash2 className="h-4 w-4"/>
            Clear
          </Button>)}
      </div>

      
      {!result && !analyzing && (<div className={`flex flex-col items-center justify-center h-[400px] border-2 border-dashed rounded-lg transition-colors ${isDragging
                ? "border-primary bg-primary/10"
                : "border-muted-foreground/30"}`} onDragOver={(e) => {
                e.preventDefault();
                setIsDragging(true);
            }} onDragLeave={(e) => {
                e.preventDefault();
                setIsDragging(false);
            }} onDrop={handleFileDrop}>
          <div className="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-muted">
            <Upload className="h-8 w-8 text-primary"/>
          </div>
          <p className="text-sm text-muted-foreground mb-4 text-center">
            {isDragging
                ? "Drop your FLAC file here"
                : "Drag and drop a FLAC file here, or click the button below to select"}
          </p>
          <Button onClick={handleSelectFile} size="lg">
            <Upload className="h-5 w-5"/>
            Select FLAC File
          </Button>
        </div>)}

      
      {analyzing && !result && (<div className="flex flex-col items-center justify-center py-16">
          <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-primary mb-4"></div>
          <p className="text-sm text-muted-foreground">Analyzing audio file...</p>
        </div>)}

      
      {result && (<div className="space-y-4">
          
          <AudioAnalysis result={result} analyzing={analyzing} showAnalyzeButton={false} filePath={selectedFilePath}/>

          
          {spectrumLoading ? (<div className="flex flex-col items-center justify-center py-16 border rounded-lg">
              <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary mb-2"></div>
              <p className="text-sm text-muted-foreground">Loading spectrum data...</p>
            </div>) : (<SpectrumVisualization sampleRate={result.sample_rate} bitsPerSample={result.bits_per_sample} duration={result.duration} spectrumData={result.spectrum}/>)}
        </div>)}
    </div>);
}
