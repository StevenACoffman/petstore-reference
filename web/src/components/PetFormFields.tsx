import React, { useState } from 'react';
import { PetStatus } from '../gen/pet/v2/pet_pb';
import { PetImage } from './PetImage';
import { acceptedPhotoTypes, mockUploadPhoto } from '../lib/photos';
import { errorMessage } from '../lib/errors';

export interface PetFormFieldsProps {
  name: string;
  species: string;
  birthDate: string;
  birthDateEstimated: boolean;
  status: PetStatus;
  tags: string;
  photoUrls: string[];
  onNameChange: (value: string) => void;
  onSpeciesChange: (value: string) => void;
  onBirthDateChange: (value: string) => void;
  onBirthDateEstimatedChange: (value: boolean) => void;
  onStatusChange: (value: PetStatus) => void;
  onTagsChange: (value: string) => void;
  onPhotoUrlsChange: (urls: string[]) => void;
}

export const PetFormFields: React.FC<PetFormFieldsProps> = (props) => {
  const [urlInput, setUrlInput] = useState('');
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [isUploading, setIsUploading] = useState(false);

  const handleAddUrl = () => {
    const trimmed = urlInput.trim();
    if (!trimmed) return;
    props.onPhotoUrlsChange([...props.photoUrls, trimmed]);
    setUrlInput('');
  };

  const handleFileUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploadError(null);
    setIsUploading(true);
    try {
      const uploadedUrl = await mockUploadPhoto(file);
      props.onPhotoUrlsChange([...props.photoUrls, uploadedUrl]);
    } catch (err: unknown) {
      setUploadError(errorMessage(err));
    } finally {
      setIsUploading(false);
      e.target.value = '';
    }
  };

  const handleRemoveUrl = (index: number) => {
    props.onPhotoUrlsChange(props.photoUrls.filter((_, i) => i !== index));
  };

  return (
    <>
      <div className="form-row">
        <div className="form-group">
          <label htmlFor="pet-name" className="required">Name</label>
          <input id="pet-name" type="text" value={props.name} onChange={(event) => props.onNameChange(event.target.value)} required maxLength={100} />
          <div className="helper-text">1 - 100 characters</div>
        </div>
        <div className="form-group">
          <label htmlFor="pet-species" className="required">Species</label>
          <input id="pet-species" type="text" value={props.species} onChange={(event) => props.onSpeciesChange(event.target.value)} required maxLength={50} />
          <div className="helper-text">1 - 50 characters</div>
        </div>
      </div>
      <div className="form-row">
        <div className="form-group">
          <label htmlFor="pet-birth-date">Birth Date</label>
          <input
            id="pet-birth-date"
            type="date"
            value={props.birthDate}
            max={new Date().toISOString().split('T')[0]}
            onChange={(event) => props.onBirthDateChange(event.target.value)}
          />
          <div className="helper-text">Optional. Leave blank if unknown.</div>
          <div className="checkbox-row" style={{ marginTop: '0.35rem' }}>
            <input
              id="pet-birth-date-estimated"
              type="checkbox"
              checked={props.birthDateEstimated}
              disabled={!props.birthDate}
              onChange={(event) => props.onBirthDateEstimatedChange(event.target.checked)}
            />
            <label htmlFor="pet-birth-date-estimated" style={{ opacity: props.birthDate ? 1 : 0.6 }}>
              This birth date is an estimate
            </label>
          </div>
        </div>
        <div className="form-group">
          <label htmlFor="pet-status">Status</label>
          <select id="pet-status" value={props.status} onChange={(event) => props.onStatusChange(Number(event.target.value) as PetStatus)}>
            <option value={PetStatus.AVAILABLE}>Available</option>
            <option value={PetStatus.PENDING}>Pending</option>
            <option value={PetStatus.ADOPTED}>Adopted</option>
          </select>
        </div>
      </div>
      <div className="form-group">
        <label htmlFor="pet-tags">Tags</label>
        <input id="pet-tags" type="text" value={props.tags} onChange={(event) => props.onTagsChange(event.target.value)} placeholder="e.g. friendly, vaccinated, playful" />
        <div className="helper-text">Separate tags with commas</div>
      </div>
      <div className="form-group">
        <label>Photos</label>
        <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
          <input
            type="url"
            placeholder="https://example.com/photo.jpg"
            value={urlInput}
            onChange={(e) => setUrlInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                handleAddUrl();
              }
            }}
            aria-label="Photo URL"
          />
          <button
            type="button"
            className="btn btn-secondary"
            onClick={handleAddUrl}
            disabled={!urlInput.trim()}
          >
            Add URL
          </button>
        </div>

        <div style={{ marginBottom: '0.75rem' }}>
          <label htmlFor="pet-upload-photo" style={{ fontSize: '0.85rem', display: 'block', marginBottom: '0.25rem' }}>
            Upload Photo
          </label>
          <input
            id="pet-upload-photo"
            type="file"
            accept={acceptedPhotoTypes.join(',')}
            onChange={handleFileUpload}
            disabled={isUploading}
          />
          <div className="helper-text">
            {isUploading ? 'Uploading photo...' : 'JPEG, PNG, WebP, or GIF up to 5MB (simulated S3 upload)'}
          </div>
          {uploadError && (
            <div style={{ color: 'var(--danger)', fontSize: '0.8rem', marginTop: '0.25rem' }}>
              {uploadError}
            </div>
          )}
        </div>

        {props.photoUrls.length > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.75rem', marginTop: '0.5rem' }}>
            {props.photoUrls.map((url, i) => (
              <div
                key={i}
                style={{
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  gap: '0.25rem',
                  border: '1px solid var(--border)',
                  borderRadius: '6px',
                  padding: '0.35rem',
                  background: 'var(--bg-secondary)',
                }}
              >
                <PetImage src={url} alt={`Photo ${i + 1}`} size={64} />
                <button
                  type="button"
                  className="btn btn-danger btn-sm"
                  onClick={() => handleRemoveUrl(i)}
                  style={{ fontSize: '0.75rem', padding: '0.1rem 0.4rem' }}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
};
