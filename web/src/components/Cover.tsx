import type { CSSProperties } from 'react'
import { fallbackColor, isSecureImage } from '../format'

interface CoverProps {
  imageURL: string
  title: string
  large?: boolean
}

export function Cover({ imageURL, title, large = false }: CoverProps) {
  const src = isSecureImage(imageURL)
  const style = { '--cover': fallbackColor(title || 'BOAI') } as CSSProperties
  return (
    <div className={`cover${large ? ' large' : ''}`} style={style} aria-hidden="true">
      {src ? (
        <img
          src={src}
          alt=""
          loading="lazy"
          referrerPolicy="no-referrer"
          onError={(event) => {
            event.currentTarget.style.display = 'none'
          }}
        />
      ) : (
        <svg>
          <use href="#cover-art" />
        </svg>
      )}
    </div>
  )
}
