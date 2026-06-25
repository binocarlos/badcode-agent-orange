// Ask-user card component.
// Copied verbatim from frontend/src/components/agent/AskUserCard.tsx.
// Fully generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useState } from 'react'
import { Box, Button, TextField, Typography } from '@mui/material'
import ArrowForwardIcon from '@mui/icons-material/ArrowForward'
import type { AskUserQuestionInfo } from '../types.js'

interface AskUserCardProps {
  question: AskUserQuestionInfo
  onAnswer: (value: string) => void
  onAdvance?: () => void
  disabled: boolean
}

export default function AskUserCard({ question, onAnswer, onAdvance, disabled }: AskUserCardProps) {
  const [freetextValue, setFreetextValue] = useState('')
  const isAnswered = question.answered

  return (
    <Box
      sx={{
        borderLeft: '3px solid #00B2FF',
        backgroundColor: 'background.default',
        borderRadius: 0,
        p: 2,
        mt: 1,
        maxWidth: 500,
      }}
    >
      <Typography sx={{ fontWeight: 600, color: 'text.primary', fontSize: '0.8125rem', mb: question.context ? 0.5 : 1 }}>
        {question.question}
      </Typography>

      {question.context && (
        <Typography sx={{ color: '#4b5563', fontSize: 13, mb: 1 }}>
          {question.context}
        </Typography>
      )}

      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
        {question.options.map(opt => {
          const isSelected = isAnswered && question.selectedValue === opt.value
          const isAdvanceOption = opt.advance && onAdvance
          return (
            <Button
              key={opt.value}
              variant={isSelected ? 'contained' : 'text'}
              color={isSelected ? 'primary' : 'inherit'}
              size="small"
              disabled={disabled || isAnswered}
              onClick={() => {
                if (isAdvanceOption) {
                  onAdvance()
                } else {
                  onAnswer(opt.value)
                }
              }}
              endIcon={isAdvanceOption ? <ArrowForwardIcon sx={{ fontSize: 16 }} /> : undefined}
              sx={{
                textTransform: 'none',
                borderRadius: 0,
                flexDirection: 'column',
                alignItems: 'flex-start',
                px: 2,
                py: opt.description ? 1 : 0.75,
                fontSize: '0.75rem',
                color: isSelected ? 'white' : 'text.secondary',
                border: '1px solid',
                borderColor: isSelected ? 'primary.main' : 'divider',
                '&:hover': {
                  backgroundColor: 'action.hover',
                  borderColor: 'rgba(0,0,0,0.15)',
                  color: 'text.primary',
                },
              }}
            >
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                <span>{opt.label}</span>
              </Box>
              {opt.description && (
                <Typography
                  component="span"
                  sx={{
                    fontSize: 11,
                    opacity: 0.8,
                    fontWeight: 400,
                    lineHeight: 1.3,
                    textAlign: 'left',
                  }}
                >
                  {opt.description}
                </Typography>
              )}
            </Button>
          )
        })}
      </Box>

      {question.allowFreetext && !isAnswered && (
        <Box sx={{ display: 'flex', gap: 1, mt: 1.5 }}>
          <TextField
            size="small"
            placeholder="Or type your answer..."
            value={freetextValue}
            onChange={e => setFreetextValue(e.target.value)}
            disabled={disabled}
            sx={{ flex: 1, '& .MuiInputBase-input': { fontSize: 13 } }}
          />
          <Button
            variant="text"
            size="small"
            disabled={disabled || !freetextValue.trim()}
            onClick={() => onAnswer(freetextValue.trim())}
            sx={{ textTransform: 'none', color: 'text.secondary', '&:hover': { backgroundColor: 'action.hover', color: 'text.primary' } }}
          >
            Submit
          </Button>
        </Box>
      )}

      {isAnswered && (
        <Typography sx={{ fontSize: '0.75rem', color: 'text.secondary', mt: 1 }}>
          Answered: {question.selectedValue}
        </Typography>
      )}
    </Box>
  )
}
