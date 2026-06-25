import { writeFile, mkdir, readFile } from 'node:fs/promises';

export interface AttachmentInput {
  mimeType: string;
  base64Content: string;
  fileName: string;
}

/** An image to embed directly in the conversation so the LLM sees it without tool calls */
export interface EmbeddedImage {
  base64: string;
  mimeType: 'image/png' | 'image/jpeg' | 'image/gif' | 'image/webp';
  label: string;
}

export interface AttachmentPromptResult {
  /** Text segments to append to the user prompt */
  promptParts: string[];
  /** Paths of slide images rendered from PPTX files */
  renderedSlides: string[];
  /** Images to embed directly in the conversation (slides rendered from PPTX) */
  embeddedImages: EmbeddedImage[];
}

/** Output shape of process_pptx.py */
interface ProcessPptxResult {
  design: Record<string, unknown>;
  style: Record<string, unknown>;
  slides: string[];
  slide_error: string | null;
}

/**
 * Process attachments: write files to disk and build prompt text describing them.
 *
 * The SDK query() only accepts string prompts (not content block arrays),
 * so all attachment context is conveyed as descriptive text with file paths.
 * Files are written to /workspace/uploads/ for tool access.
 *
 * For PPTX files, runs process_pptx.py which extracts design/style data AND
 * renders slides. The prompt instructs the agent to view_image each slide
 * so the LLM can see the presentation visually.
 *
 * @param attachments  Array of base64-encoded file attachments
 * @param processPptx  Optional function override for testing
 * @returns Prompt parts and rendered slide paths
 */
export async function processAttachments(
  attachments: AttachmentInput[],
  processPptx?: (uploadPath: string) => Promise<ProcessPptxResult>,
  progress?: (phase: string, label: string) => void,
): Promise<AttachmentPromptResult> {
  const promptParts: string[] = [];
  const renderedSlides: string[] = [];
  const embeddedImages: EmbeddedImage[] = [];

  const defaultProcessPptx = async (uploadPath: string): Promise<ProcessPptxResult> => {
    const { execSync } = await import('node:child_process');
    const result = execSync(
      `python3 /workspace/lib/process_pptx.py ${JSON.stringify(uploadPath)}`,
      { timeout: 120000, encoding: 'utf-8', cwd: '/workspace' },
    );
    return JSON.parse(result.trim());
  };

  const processFn = processPptx ?? defaultProcessPptx;

  for (const att of attachments) {
    const fileName = att.fileName || 'upload';
    const uploadDir = '/workspace/uploads';
    const uploadPath = `${uploadDir}/${fileName}`;

    // Write file to sandbox filesystem
    try {
      progress?.('processing', `Saving uploaded file: ${fileName}`);
      await mkdir(uploadDir, { recursive: true });
      await writeFile(uploadPath, Buffer.from(att.base64Content, 'base64'));
      console.log(`[processAttachments] Wrote file to sandbox: ${uploadPath}`);
    } catch (err) {
      console.error(`[processAttachments] Failed to write ${uploadPath}: ${err}`);
    }

    // Build prompt text based on file type
    if (att.mimeType.startsWith('image/')) {
      promptParts.push(
        `[Uploaded image: ${fileName} → ${uploadPath}]\nUse Bash to view this image or process it with Python.`
      );
    } else if (att.mimeType === 'application/pdf') {
      promptParts.push(
        `[Uploaded PDF: ${fileName} → ${uploadPath}]\nUse Python to extract content from this PDF.`
      );
    } else if (att.mimeType.startsWith('text/') || att.mimeType === 'application/json') {
      const text = Buffer.from(att.base64Content, 'base64').toString('utf-8');
      promptParts.push(`[File: ${fileName} → ${uploadPath}]\n${text}`);
    } else if (
      att.mimeType === 'application/vnd.openxmlformats-officedocument.presentationml.presentation'
      || fileName.toLowerCase().endsWith('.pptx')
    ) {
      // Process PPTX: extract design/style AND render slides in one command
      try {
        progress?.('processing', 'Extracting design and rendering slides...');
        const pptxResult = await processFn(uploadPath);
        console.log(`[processAttachments] process_pptx.py returned: slides=${pptxResult.slides.length}, slide_error=${pptxResult.slide_error}`);

        // Design data is always available (pure python-pptx, no LibreOffice)
        const designJson = JSON.stringify(pptxResult.design, null, 2);
        const styleJson = JSON.stringify(pptxResult.style, null, 2);

        if (pptxResult.slides.length > 0) {
          // Slides rendered successfully
          renderedSlides.push(...pptxResult.slides);
          progress?.('processing', `Processed ${pptxResult.slides.length} slides`);

          // Read slide images and embed them directly so the LLM sees them
          // without needing view_image tool calls
          progress?.('processing', 'Embedding slide images for AI...');
          const pptxBaseName = fileName.replace(/\.pptx$/i, '')
          for (let i = 0; i < pptxResult.slides.length; i++) {
            try {
              const imgData = await readFile(pptxResult.slides[i]);
              embeddedImages.push({
                base64: imgData.toString('base64'),
                mimeType: 'image/png',
                label: `${pptxBaseName} - Slide ${i + 1}`,
              });
            } catch {
              console.warn(`[processAttachments] Could not read slide image: ${pptxResult.slides[i]}`);
            }
          }

          const slideList = pptxResult.slides.map((sp, i) => `  ${pptxBaseName} Slide ${i + 1}: ${sp}`).join('\n');
          const imageNote = embeddedImages.length > 0
            ? `The slide images are included in this conversation -- you can see them directly.`
            : `Use view_image on each slide to see the presentation visually.`;
          promptParts.push(
            `[PPTX: ${fileName} → ${uploadPath}]\n` +
            `${pptxResult.slides.length} slides rendered as images. ${imageNote}\n` +
            `${slideList}\n\n` +
            `Design spec (extracted from theme XML):\n\`\`\`json\n${designJson}\n\`\`\`\n\n` +
            `Style spec (derived palette):\n\`\`\`json\n${styleJson}\n\`\`\``
          );
          console.log(`[processAttachments] Auto-converted PPTX to ${pptxResult.slides.length} slide images (${embeddedImages.length} embedded) with design data`);
        } else {
          // Slides failed but design data still available
          const errorNote = pptxResult.slide_error || 'Unknown error';
          promptParts.push(
            `[PPTX: ${fileName} → ${uploadPath}]\n` +
            `Slide rendering failed: ${errorNote}\n` +
            `To retry: python3 /workspace/lib/process_pptx.py '${uploadPath}'\n\n` +
            `Design spec (extracted from theme XML):\n\`\`\`json\n${designJson}\n\`\`\`\n\n` +
            `Style spec (derived palette):\n\`\`\`json\n${styleJson}\n\`\`\``
          );
          console.log(`[processAttachments] PPTX slide rendering failed but design data extracted`);
        }
      } catch (err) {
        console.error(`[processAttachments] process_pptx.py failed:`, err);
        promptParts.push(
          `[PPTX: ${fileName} → ${uploadPath}]\nProcessing failed. Run: python3 /workspace/lib/process_pptx.py '${uploadPath}'\nThis extracts theme colors, fonts, layouts and attempts slide rendering.`
        );
      }
    } else {
      promptParts.push(
        `[Uploaded file: ${fileName} → ${uploadPath}]\nFile is available for processing with Python tools.`
      );
    }
  }

  return { promptParts, renderedSlides, embeddedImages };
}
